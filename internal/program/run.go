package program

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/brijorn/mast/internal/node"
	"github.com/google/uuid"
)

const runSchemaVersion = 2

func syncLegacyAutostart(run *Run) {
	run.Autostart = run.AutostartReconnect || run.AutostartCrashRestart
}

func migrateRunAutostart(run *Run) bool {
	if run.SchemaVersion >= 2 {
		syncLegacyAutostart(run)
		return false
	}
	run.AutostartReconnect = run.Autostart
	run.AutostartCrashRestart = run.Autostart
	syncLegacyAutostart(run)
	return true
}

func cloneRun(run *Run) Run {
	clone := *run
	clone.Env = make(map[string]string, len(run.Env))
	for key, value := range run.Env {
		clone.Env[key] = value
	}
	clone.CmdArgs = append([]string(nil), run.CmdArgs...)
	clone.Companions = append([]RunProcess(nil), run.Companions...)
	for index := range clone.Companions {
		clone.Companions[index].CmdArgs = append([]string(nil), run.Companions[index].CmdArgs...)
	}
	if run.AutostartSupervisor != nil {
		supervisor := *run.AutostartSupervisor
		if run.AutostartSupervisor.LastFailureAt != nil {
			lastFailureAt := *run.AutostartSupervisor.LastFailureAt
			supervisor.LastFailureAt = &lastFailureAt
		}
		if run.AutostartSupervisor.NextRestartAt != nil {
			nextRestartAt := *run.AutostartSupervisor.NextRestartAt
			supervisor.NextRestartAt = &nextRestartAt
		}
		clone.AutostartSupervisor = &supervisor
	}
	return clone
}

func nextRunSnapshot(run *Run) Run {
	syncLegacyAutostart(run)
	run.SchemaVersion = runSchemaVersion
	run.Revision++
	return cloneRun(run)
}

func (s *Store) ListRuns() []Run {
	s.reconcileActiveRunProcesses()

	s.mu.Lock()
	defer s.mu.Unlock()

	runs := make([]Run, 0, len(s.runs))
	for _, state := range s.runs {
		run := cloneRun(state.run)
		if !state.checkpointPolledAt.IsZero() {
			polled := state.checkpointPolledAt
			run.CheckpointPolledAt = &polled
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.Before(runs[j].StartedAt)
	})
	return runs
}

func (s *Store) reconcileActiveRunProcesses() {
	var changed []Run

	s.mu.Lock()
	for _, state := range s.runs {
		run := state.run
		if state.stopping || !runIsActive(run) || run.PID <= 0 {
			continue
		}
		alive, matches := runProcessStatus(run)
		if alive && matches {
			continue
		}
		if alive {
			markRunLost(run, "process pid is now owned by another process")
		} else {
			markRunLost(run, "process finished before Mast collected exit status")
			clearRunPID(run)
		}
		changed = append(changed, nextRunSnapshot(run))
	}
	s.mu.Unlock()

	for _, run := range changed {
		writeRunJSONBestEffort(filepath.Join(run.Workspace, "run.json"), &run)
	}
}

func (s *Store) Start(opts StartOptions) ([]Run, error) {
	if opts.ProgramID == "" {
		return nil, errors.New("program_id required")
	}
	if len(opts.Serials) == 0 {
		return nil, errors.New("at least one serial required")
	}

	s.mu.Lock()
	p, ok := s.programs[opts.ProgramID]
	if !ok {
		// Accept a slug in place of a content-hash ID.
		p, ok = s.programBySlugLocked(opts.ProgramID)
	}
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("program not found")
	}

	nodes := s.devices.ListNodes()

	var runs []Run
	for _, serial := range opts.Serials {
		device, err := s.devices.DeviceBySerial(serial)
		if err != nil {
			return nil, err
		}

		run, err := s.startOne(p, *device, nodes, opts.Variables, opts.SecretVariables)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, nil
}

func (s *Store) Stop(opts StopOptions) (*Run, error) {
	s.mu.Lock()
	state := s.runs[opts.ID]
	if state == nil {
		s.mu.Unlock()
		return nil, errors.New("run not found")
	}
	state.run.AutostartPaused = opts.AutostartPaused
	if state.cmd == nil || state.cmd.Process == nil {
		markRunStopped(state.run)
		run := nextRunSnapshot(state.run)
		s.mu.Unlock()
		writeRunJSONBestEffort(filepath.Join(run.Workspace, "run.json"), &run)
		return &run, nil
	}
	state.stopping = true
	if state.run.PID == 0 {
		setRunPID(state.run, state.cmd.Process.Pid)
	}
	run := nextRunSnapshot(state.run)
	s.mu.Unlock()
	writeRunJSONBestEffort(filepath.Join(run.Workspace, "run.json"), &run)
	if err := killRunProcess(&run); err != nil {
		if alive, _ := runProcessStatus(&run); !alive {
			s.mu.Lock()
			state := s.runs[opts.ID]
			if state == nil {
				s.mu.Unlock()
				return nil, errors.New("run not found")
			}
			markRunStopped(state.run)
			run = nextRunSnapshot(state.run)
			s.mu.Unlock()
			writeRunJSONBestEffort(filepath.Join(run.Workspace, "run.json"), &run)
			return &run, nil
		}
		return nil, err
	}
	return &run, nil
}

func (s *Store) RequestStop(id string) (*Run, error) {
	s.mu.Lock()
	state := s.runs[id]
	if state == nil {
		s.mu.Unlock()
		return nil, errors.New("run not found")
	}
	if !runIsActive(state.run) {
		s.mu.Unlock()
		return nil, errors.New("run is not active")
	}
	if state.run.StopRequestedAt == nil {
		now := time.Now().UTC()
		state.run.StopRequestedAt = &now
		state.run.StopAcknowledgedAt = nil
	}
	run := nextRunSnapshot(state.run)
	s.mu.Unlock()
	if err := writeRunJSON(filepath.Join(run.Workspace, "run.json"), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// StopRequest answers the program's own question, asked from its checkpoint
// loop: is a stop pending? Asking is what identifies a program that can be
// stopped softly at all, so the question is recorded as well as answered.
func (s *Store) StopRequest(id string) (*StopRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.runs[id]
	if state == nil {
		return nil, errors.New("run not found")
	}
	state.checkpointPolledAt = time.Now().UTC()
	return &StopRequest{RequestedAt: state.run.StopRequestedAt, AcknowledgedAt: state.run.StopAcknowledgedAt}, nil
}

func (s *Store) AcknowledgeStop(id string) (*Run, error) {
	s.mu.Lock()
	state := s.runs[id]
	if state == nil {
		s.mu.Unlock()
		return nil, errors.New("run not found")
	}
	if state.run.StopRequestedAt == nil {
		s.mu.Unlock()
		return nil, errors.New("stop has not been requested")
	}
	if state.run.StopAcknowledgedAt == nil {
		now := time.Now().UTC()
		state.run.StopAcknowledgedAt = &now
	}
	run := nextRunSnapshot(state.run)
	s.mu.Unlock()
	if err := writeRunJSON(filepath.Join(run.Workspace, "run.json"), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func markRunStopped(run *Run) {
	if !runIsActive(run) {
		return
	}
	now := time.Now().UTC()
	run.Status = RunStatusStopped
	run.CompletedAt = &now
	run.ExitCode = nil
	run.Error = ""
	clearRunPID(run)
}

func markRunLost(run *Run, message string) {
	if !runIsActive(run) {
		return
	}
	run.Status = RunStatusLost
	run.CompletedAt = nil
	run.ExitCode = nil
	run.Error = message
}

func runIsActive(run *Run) bool {
	return run.Status == RunStatusRunning || run.Status == RunStatusStarting
}

func (s *Store) SetRunAutostart(id string, enabled bool) (*Run, error) {
	return s.UpdateRunAutostart(id, AutostartOptions{
		Reconnect:    &enabled,
		CrashRestart: &enabled,
	})
}

func (s *Store) UpdateRunAutostart(id string, opts AutostartOptions) (*Run, error) {
	if opts.Reconnect == nil && opts.CrashRestart == nil {
		return nil, errors.New("at least one autostart behavior is required")
	}

	s.mu.Lock()
	state := s.runs[id]
	if state == nil {
		s.mu.Unlock()
		return nil, errors.New("run not found")
	}
	enabling := (opts.Reconnect != nil && *opts.Reconnect) ||
		(opts.CrashRestart != nil && *opts.CrashRestart)
	if enabling {
		if state.run.WorkspaceCleaned {
			s.mu.Unlock()
			return nil, errors.New("workspace has been cleaned up")
		}
		if state.run.Cmd == "" {
			s.mu.Unlock()
			return nil, errors.New("run has no persisted command")
		}
	}
	if opts.Reconnect != nil {
		state.run.AutostartReconnect = *opts.Reconnect
	}
	if opts.CrashRestart != nil {
		state.run.AutostartCrashRestart = *opts.CrashRestart
		if !*opts.CrashRestart {
			state.run.AutostartSupervisor = nil
			delete(s.autostartRestarts, id)
		}
	}
	syncLegacyAutostart(state.run)
	if !state.run.Autostart {
		state.run.AutostartPaused = false
	}
	run := nextRunSnapshot(state.run)
	s.mu.Unlock()

	if err := writeRunJSON(filepath.Join(run.Workspace, "run.json"), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) Shutdown() {
	s.shuttingDown.Store(true)
	s.monitorCancel()
	s.mu.Lock()
	states := make([]*runState, 0, len(s.runs))
	runs := make([]Run, 0, len(s.runs))
	for _, state := range s.runs {
		if state.cmd != nil && state.cmd.Process != nil &&
			(state.run.Status == RunStatusRunning || state.run.Status == RunStatusStarting) {
			state.stopping = true
			if state.run.PID == 0 {
				setRunPID(state.run, state.cmd.Process.Pid)
			}
			states = append(states, state)
			runs = append(runs, cloneRun(state.run))
		}
	}
	s.mu.Unlock()

	for index := range runs {
		_ = killRunProcess(&runs[index])
	}
	for index := range runs {
		_ = waitForRunProcessExit(&runs[index], 2*time.Second)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		allStopped := true
		s.mu.Lock()
		for _, state := range states {
			if state.run.Status == RunStatusRunning || state.run.Status == RunStatusStarting {
				allStopped = false
				break
			}
		}
		s.mu.Unlock()
		if allStopped {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func configTemplatePath(workspace string, configFile string) string {
	return filepath.Join(workspace, ".mast", "config-templates", configFile)
}

func secretVariablesPath(workspace string) string {
	return filepath.Join(workspace, ".mast", "secret-variables.json")
}

func readSecretVariables(workspace string) (map[string]string, error) {
	data, err := os.ReadFile(secretVariablesPath(workspace))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var variables map[string]string
	if err := json.Unmarshal(data, &variables); err != nil {
		return nil, err
	}
	return variables, nil
}

func writeSecretVariables(workspace string, variables map[string]string) error {
	if len(variables) == 0 {
		return nil
	}
	path := secretVariablesPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(variables)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func buildRunVariables(mappings []ConfigMapping, overrides map[string]string, device node.DeviceInfo) map[string]string {
	variables := make(map[string]string)
	for _, mapping := range mappings {
		if mapping.Key == "" {
			continue
		}
		variables[mapping.Key] = resolveValue(mapping.Value, overrides, device)
	}
	for key, value := range overrides {
		variables[key] = value
	}
	return variables
}

func mergeVariables(base map[string]string, overrides map[string]string) map[string]string {
	merged := make(map[string]string)
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return merged
}

func companionEnabled(companion CompanionEntry, variables map[string]string) bool {
	variable := strings.TrimSpace(companion.EnabledWhen.Variable)
	if variable == "" {
		return true
	}
	want := strings.TrimSpace(companion.EnabledWhen.Equals)
	got, ok := variables[variable]
	if !ok {
		got = variables[strings.ToLower(variable)]
	}
	return strings.EqualFold(strings.TrimSpace(got), want)
}

func (s *Store) resolveRunCommand(workspace, command string, args []string) (string, []string, error) {
	if localCommand := filepath.Join(workspace, command); fileExists(localCommand) {
		if err := ensureLocalEntryExecutable(localCommand); err != nil {
			return "", nil, err
		}
		command = localCommand
	}
	return s.runnerCommand(command, args)
}

func (s *Store) startRunProcesses(state *runState, stdout, stderr io.Writer, env map[string]string) error {
	run := state.run
	s.mu.Lock()
	command := run.Cmd
	args := append([]string(nil), run.CmdArgs...)
	workspace := run.Workspace
	runID := run.ID
	s.mu.Unlock()

	command, args = scopedCommand(runID, command, args)
	cmd := s.startCmd(command, args...)
	configureRunCommand(cmd)
	cmd.Dir = workspace
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = mergeEnv(os.Environ(), env)
	if err := cmd.Start(); err != nil {
		return err
	}
	s.mu.Lock()
	state.cmd = cmd
	setRunPID(run, cmd.Process.Pid)
	s.mu.Unlock()

	for index := range run.Companions {
		s.mu.Lock()
		process := run.Companions[index]
		mainPID := run.PID
		s.mu.Unlock()
		companionCommand, companionArgs := scopedCommand(runID, process.Cmd, process.CmdArgs)
		companionCmd := s.startCmd(companionCommand, companionArgs...)
		configureCompanionRunCommand(companionCmd, mainPID)
		companionCmd.Dir = workspace
		companionCmd.Stdout = stdout
		companionCmd.Stderr = stderr
		companionCmd.Env = mergeEnv(os.Environ(), env)
		if err := companionCmd.Start(); err != nil {
			if !process.Required {
				s.mu.Lock()
				run.Companions[index].Error = err.Error()
				s.mu.Unlock()
				continue
			}
			s.mu.Lock()
			state.mainExited = true
			runForStop := cloneRun(run)
			s.mu.Unlock()
			_ = killRunProcess(&runForStop)
			_ = cmd.Wait()
			state.companionWG.Wait()
			return fmt.Errorf("start companion %s: %w", process.ID, err)
		}
		s.mu.Lock()
		run.Companions[index].PID = companionCmd.Process.Pid
		run.Companions[index].Error = ""
		state.companionCmds = append(state.companionCmds, companionCmd)
		state.companionWG.Add(1)
		s.mu.Unlock()
		go s.waitCompanion(state, process.ID, process.Required, companionCmd)
	}
	return nil
}

func (s *Store) waitCompanion(state *runState, id string, required bool, cmd *exec.Cmd) {
	err := cmd.Wait()
	state.companionWG.Done()
	if !required {
		return
	}

	s.mu.Lock()
	if state.stopping || state.mainExited || !runIsActive(state.run) {
		s.mu.Unlock()
		return
	}
	message := fmt.Sprintf("required companion %s exited", id)
	if err != nil {
		message += ": " + err.Error()
	}
	state.companionFailure = message
	run := cloneRun(state.run)
	s.mu.Unlock()
	_ = killRunProcess(&run)
}

func (s *Store) programForRun(run *Run) Program {
	s.mu.Lock()
	if p, ok := s.programs[run.ProgramID]; ok {
		s.mu.Unlock()
		return p
	}
	s.mu.Unlock()
	f, err := os.Open(filepath.Join(run.Workspace, "mast-program.json"))
	if err != nil {
		return Program{}
	}
	defer func() { _ = f.Close() }()

	var p Program
	if err := json.NewDecoder(f).Decode(&p); err == nil {
		return p
	}
	return Program{}
}

// Resume re-executes a completed, failed, stopped, or lost run in its existing
// workspace, preserving the run ID. A failed attempt's logs are rotated before
// the current logs are replaced.
// The run's Cmd and CmdArgs must have been persisted when the run was
// originally started.
func (s *Store) Resume(opts ResumeOptions) (*Run, error) {
	s.mu.Lock()
	state := s.runs[opts.ID]
	if state == nil {
		s.mu.Unlock()
		return nil, errors.New("run not found")
	}
	run := state.run
	if run.Status == RunStatusRunning || run.Status == RunStatusStarting {
		s.mu.Unlock()
		return nil, errors.New("run is already active")
	}
	if state.resuming {
		s.mu.Unlock()
		return nil, errors.New("run resume is already in progress")
	}
	if run.WorkspaceCleaned {
		s.mu.Unlock()
		return nil, errors.New("workspace has been cleaned up")
	}
	if run.Cmd == "" {
		s.mu.Unlock()
		return nil, errors.New("run has no persisted command")
	}
	savedRun := cloneRun(run)
	rotateFailedLogs := run.Status == RunStatusFailed
	state.resuming = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		state.resuming = false
		s.mu.Unlock()
	}()

	// Stop the run's own process before restarting it — but only when the saved
	// PID is still alive AND still that process. A PID that is alive but no longer
	// matches was reused by an unrelated process since this run exited: the run's
	// process is gone, so resume proceeds without touching the stranger. Erroring
	// there wedged a resumed run whose PID a macOS process had recycled, and the
	// reconnect supervisor retried it forever — "keeps getting a process id error"
	// while trying to re-own the device.
	if alive, matches := runProcessStatus(&savedRun); alive && matches {
		if err := killRunProcess(&savedRun); err != nil {
			return nil, err
		}
		if !waitForRunProcessExit(&savedRun, 2*time.Second) {
			return nil, fmt.Errorf("run pid %d is still alive", savedRun.PID)
		}
	}

	device := node.DeviceInfo{Serial: savedRun.Serial, NodeID: savedRun.NodeID}
	if devices, err := s.devices.ListDevices(); err == nil {
		for _, candidate := range devices {
			if candidate.Serial == savedRun.Serial {
				device = candidate
				break
			}
		}
	}

	p := s.programForRun(&savedRun)
	variables := mergeVariables(savedRun.Env, opts.Variables)
	secretVariables, err := readSecretVariables(savedRun.Workspace)
	if err != nil {
		return nil, err
	}
	secretVariables = mergeVariables(secretVariables, opts.SecretVariables)
	if err := writeSecretVariables(savedRun.Workspace, secretVariables); err != nil {
		return nil, err
	}
	renderVariables := mergeVariables(variables, secretVariables)
	if p.ConfigFile != "" {
		configPath := filepath.Join(savedRun.Workspace, p.ConfigFile)
		templatePath := configTemplatePath(savedRun.Workspace, p.ConfigFile)
		if !fileExists(templatePath) {
			bundleConfigPath := filepath.Join(s.bundlePath(savedRun.ProgramID), p.ConfigFile)
			if fileExists(bundleConfigPath) {
				if err := os.MkdirAll(filepath.Dir(templatePath), 0700); err != nil {
					return nil, err
				}
				if err := copyFile(bundleConfigPath, templatePath, 0600); err != nil {
					return nil, err
				}
			}
		}
		if fileExists(templatePath) {
			if err := copyFile(templatePath, configPath, 0600); err != nil {
				return nil, err
			}
		}
		if err := applyConfigReplacements(configPath, p.ConfigMappings, renderVariables, device); err != nil {
			return nil, err
		}
	}

	// Start a fresh log stream for the resumed attempt.
	if rotateFailedLogs {
		if err := rotateRunLogs(savedRun.Workspace); err != nil {
			return nil, err
		}
	}
	stdout, err := s.newRunLogWriter(run, filepath.Join(savedRun.Workspace, "stdout.log"), "stdout")
	if err != nil {
		return nil, err
	}
	stderr, err := s.newRunLogWriter(run, filepath.Join(savedRun.Workspace, "stderr.log"), "stderr")
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}

	env := withDefaultRunEnv(variables)

	s.mu.Lock()
	// The resumed configuration becomes the run's, rather than applying to this
	// attempt alone. A crash-restart supervisor resumes with no variables of its
	// own, so a value that lived only in this call would be reverted by the
	// first crash — and a run silently back on the configuration its operator
	// replaced is worse than one that never took the change.
	run.Env = variables
	run.StdoutLogStart = 0
	run.StderrLogStart = 0
	if !opts.Supervisor {
		run.AutostartSupervisor = nil
		delete(s.autostartRestarts, run.ID)
	}
	run.Status = RunStatusStarting
	run.AutostartPaused = false
	run.StopRequestedAt = nil
	run.StopAcknowledgedAt = nil
	run.ExitCode = nil
	run.Error = ""
	run.CompletedAt = nil
	clearRunPID(run)
	for index := range run.Companions {
		run.Companions[index].PID = 0
		run.Companions[index].Error = ""
	}
	run.StartedAt = time.Now().UTC()
	state.stopping = false
	state.mainExited = false
	state.companionFailure = ""
	state.companionCmds = nil
	state.checkpointPolledAt = time.Time{}
	startingSnapshot := nextRunSnapshot(run)
	s.mu.Unlock()

	writeRunJSONBestEffort(filepath.Join(startingSnapshot.Workspace, "run.json"), &startingSnapshot)

	if err := s.startRunProcesses(state, stdout, stderr, env); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		s.mu.Lock()
		run.Status = RunStatusFailed
		run.Error = err.Error()
		now := time.Now().UTC()
		run.CompletedAt = &now
		failedSnapshot := nextRunSnapshot(run)
		s.mu.Unlock()
		writeRunJSONBestEffort(filepath.Join(failedSnapshot.Workspace, "run.json"), &failedSnapshot)
		return nil, err
	}

	s.mu.Lock()
	run.Status = RunStatusRunning
	runningSnapshot := nextRunSnapshot(run)
	s.mu.Unlock()
	writeRunJSONBestEffort(filepath.Join(runningSnapshot.Workspace, "run.json"), &runningSnapshot)

	go s.waitRun(state, stdout, stderr)
	return run, nil
}

// free disk space. Returns an error if the run is still active. Sets
// WorkspaceCleaned on the run once the workspace has been removed.
func (s *Store) CleanupRun(id string) (*Run, error) {
	s.mu.Lock()
	state := s.runs[id]
	s.mu.Unlock()
	if state == nil {
		return nil, errors.New("run not found")
	}
	run := state.run
	if run.Status == RunStatusRunning || run.Status == RunStatusStarting {
		return nil, errors.New("cannot clean up an active run")
	}
	if run.Status == RunStatusLost {
		alive, matches := runProcessStatus(run)
		if alive && matches {
			return nil, errors.New("cannot clean up a lost run whose process is still alive")
		}
	}
	if !run.WorkspaceCleaned {
		if err := os.RemoveAll(run.Workspace); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		s.mu.Lock()
		run.WorkspaceCleaned = true
		s.mu.Unlock()
	}
	return run, nil
}

func (s *Store) startOne(p Program, device node.DeviceInfo, nodes []node.NodeInfo, variables map[string]string, secretVariables map[string]string) (*Run, error) {
	id := uuid.NewString()
	workspace := filepath.Join(s.instanceDir(), id)
	if err := copyDir(s.bundlePath(p.ID), workspace); err != nil {
		return nil, err
	}
	runVariables := buildRunVariables(p.ConfigMappings, variables, device)
	if err := writeSecretVariables(workspace, secretVariables); err != nil {
		return nil, err
	}
	renderVariables := mergeVariables(runVariables, secretVariables)
	if p.ConfigFile != "" {
		configPath := filepath.Join(workspace, p.ConfigFile)
		templatePath := configTemplatePath(workspace, p.ConfigFile)
		if err := os.MkdirAll(filepath.Dir(templatePath), 0700); err != nil {
			return nil, err
		}
		if err := copyFile(configPath, templatePath, 0600); err != nil {
			return nil, err
		}
		if err := applyConfigReplacements(configPath, p.ConfigMappings, renderVariables, device); err != nil {
			return nil, err
		}
	}

	env := defaultRunEnv()
	for key, value := range s.standardDeviceEnv(device) {
		env[key] = value
	}
	for key, value := range adbEnv(device, nodes) {
		env[key] = value
	}
	for key, value := range runVariables {
		env[key] = value
	}
	// Applied after the program's own variables, not before: the MAST_ names
	// are Mast's answer to who this run is, and a program that declares one
	// cannot be allowed to answer for it.
	env["MAST_RUN_ID"] = id
	env["MAST_DEVICE_ID"] = device.Serial

	command := p.Entry.Command
	resolvedArgs := make([]string, len(p.Entry.Args))
	for i, arg := range p.Entry.Args {
		resolvedArgs[i] = resolveValue(arg, runVariables, device)
	}
	command, args, err := s.resolveRunCommand(workspace, command, resolvedArgs)
	if err != nil {
		return nil, err
	}

	run := &Run{
		SchemaVersion: runSchemaVersion,
		Revision:      1,
		ID:            id,
		ProgramID:     p.ID,
		Serial:        device.Serial,
		NodeID:        device.NodeID,
		Workspace:     workspace,
		Status:        RunStatusStarting,
		Env:           env,
		Cmd:           command,
		CmdArgs:       args,
		StartedAt:     time.Now().UTC(),
	}
	for _, companion := range p.Entry.Companions {
		if !companionEnabled(companion, runVariables) {
			continue
		}
		resolvedCompanionArgs := make([]string, len(companion.Args))
		for index, arg := range companion.Args {
			resolvedCompanionArgs[index] = resolveValue(arg, runVariables, device)
		}
		companionCommand, companionArgs, err := s.resolveRunCommand(workspace, companion.Command, resolvedCompanionArgs)
		if err != nil {
			return nil, fmt.Errorf("resolve companion %s: %w", companion.ID, err)
		}
		run.Companions = append(run.Companions, RunProcess{
			ID: companion.ID, Cmd: companionCommand, CmdArgs: companionArgs, Required: companion.Required,
		})
	}
	stdout, err := s.newRunLogWriter(run, filepath.Join(workspace, "stdout.log"), "stdout")
	if err != nil {
		return nil, err
	}
	stderr, err := s.newRunLogWriter(run, filepath.Join(workspace, "stderr.log"), "stderr")
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}
	state := &runState{run: run}
	if err := writeRunJSON(filepath.Join(workspace, "run.json"), run); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}

	if err := s.startRunProcesses(state, stdout, stderr, env); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}

	s.mu.Lock()
	run.Status = RunStatusRunning
	s.runs[id] = state
	runSnapshot := nextRunSnapshot(run)
	s.mu.Unlock()
	writeRunJSONBestEffort(filepath.Join(workspace, "run.json"), &runSnapshot)

	go s.waitRun(state, stdout, stderr)
	return run, nil
}

func (s *Store) waitRun(state *runState, stdout, stderr io.Closer) {
	err := state.cmd.Wait()
	s.mu.Lock()
	state.mainExited = true
	runForStop := cloneRun(state.run)
	s.mu.Unlock()
	if len(state.companionCmds) > 0 {
		_ = killRunProcess(&runForStop)
		state.companionWG.Wait()
	}
	_ = stdout.Close()
	_ = stderr.Close()

	now := time.Now().UTC()
	s.mu.Lock()
	state.run.CompletedAt = &now
	clearRunPID(state.run)
	for index := range state.run.Companions {
		state.run.Companions[index].PID = 0
	}
	// Mast going down is not a decision about the run. Recording it as
	// `stopped` makes it indistinguishable from a stop somebody asked for, and
	// the startup resume — which exists to bring back exactly the runs the
	// daemon took down with it — would relaunch a run an operator had
	// deliberately stopped. `lost` is what a Mast killed outright already
	// leaves behind, so both shutdowns now read the same.
	if s.shuttingDown.Load() {
		state.run.ExitCode = nil
		state.run.Status = RunStatusLost
		state.run.CompletedAt = nil
		state.run.Error = "mast restarted; run was stopped for shutdown"
	} else if state.stopping || state.run.StopRequestedAt != nil {
		// A run that ends after a stop was requested ended because it was asked
		// to, whether the kill did it or the program reached its own checkpoint
		// first. Recording that as `exited` loses the only thing that
		// distinguishes it from a program finishing a session by itself, and
		// the crash-restart watch — which exists to resume exactly that — would
		// relaunch a run the operator just stopped.
		state.run.ExitCode = nil
		state.run.Status = RunStatusStopped
		state.run.Error = ""
	} else if state.companionFailure != "" {
		state.run.ExitCode = nil
		state.run.Status = RunStatusFailed
		state.run.Error = state.companionFailure
	} else if err == nil {
		code := 0
		state.run.ExitCode = &code
		state.run.Status = RunStatusExited
	} else if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		state.run.ExitCode = &code
		state.run.Status = RunStatusFailed
		state.run.Error = err.Error()
	} else {
		state.run.Status = RunStatusFailed
		state.run.Error = err.Error()
	}
	completedSnapshot := nextRunSnapshot(state.run)
	writeRunJSONBestEffort(filepath.Join(completedSnapshot.Workspace, "run.json"), &completedSnapshot)
	s.mu.Unlock()
}

// loadRuns scans the instances directory and restores run state from persisted
// run.json files. Any run whose status was active is marked lost because Mast
// no longer owns the process handle after a daemon restart.
func (s *Store) loadRuns() {
	entries, err := os.ReadDir(s.instanceDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runFile := filepath.Join(s.instanceDir(), entry.Name(), "run.json")
		data, err := os.ReadFile(runFile)
		if err != nil {
			continue
		}
		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			continue
		}
		changed := migrateRunAutostart(&run)
		if run.Status == RunStatusRunning || run.Status == RunStatusStarting {
			alive, matches := runProcessStatus(&run)
			run.Status = RunStatusLost
			run.CompletedAt = nil
			switch {
			case alive && matches:
				run.Error = "mast restarted; process is still running unmanaged"
			case alive:
				run.Error = "mast restarted; saved pid is now owned by another process"
			default:
				run.Error = "mast restarted; process ownership was lost"
			}
			changed = true
		}
		if changed {
			snapshot := nextRunSnapshot(&run)
			writeRunJSONBestEffort(runFile, &snapshot)
		}
		s.runs[run.ID] = &runState{run: &run}
	}
}

func (s *Store) resumeAutostartRuns() {
	s.resumeAutostartRunIDs(s.autostartRunIDsForStartup(), "autostart resume failed")
}

func (s *Store) SetRunners(runners map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runners = runners
}

func (s *Store) runnerCommand(command string, args []string) (string, []string, error) {
	s.mu.Lock()
	runners := s.runners
	s.mu.Unlock()

	var runner string
	ext := filepath.Ext(command)
	if runners != nil {
		if r, ok := runners[ext]; ok && r != "" {
			runner = r
		}
	}

	if runner != "" {
		parts, err := splitRunnerCommand(runner)
		if err != nil {
			return "", nil, fmt.Errorf("invalid runner for %s: %w", ext, err)
		}
		if len(parts) > 0 {
			return parts[0], append(append(parts[1:], command), args...), nil
		}
	}

	if filepath.Ext(command) == ".exe" && runtime.GOOS != "windows" {
		return "", nil, fmt.Errorf("no runner configured for non-native executable %q", command)
	}
	return command, args, nil
}

func ensureLocalEntryExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode()
	if !mode.IsRegular() || mode&0100 != 0 {
		return nil
	}
	return os.Chmod(path, mode|0100)
}
