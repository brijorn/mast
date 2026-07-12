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

const runSchemaVersion = 1

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
	return clone
}

func nextRunSnapshot(run *Run) Run {
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
			run.PID = 0
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

		run, err := s.startOne(p, *device, nodes, opts.Variables)
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
		state.run.PID = state.cmd.Process.Pid
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

func (s *Store) StopRequest(id string) (*StopRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.runs[id]
	if state == nil {
		return nil, errors.New("run not found")
	}
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
	run.PID = 0
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
	s.mu.Lock()
	state := s.runs[id]
	if state == nil {
		s.mu.Unlock()
		return nil, errors.New("run not found")
	}
	if enabled {
		if state.run.WorkspaceCleaned {
			s.mu.Unlock()
			return nil, errors.New("workspace has been cleaned up")
		}
		if state.run.Cmd == "" {
			s.mu.Unlock()
			return nil, errors.New("run has no persisted command")
		}
	} else {
		state.run.AutostartPaused = false
	}
	state.run.Autostart = enabled
	run := nextRunSnapshot(state.run)
	s.mu.Unlock()

	if err := writeRunJSON(filepath.Join(run.Workspace, "run.json"), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) Shutdown() {
	s.monitorCancel()
	s.mu.Lock()
	states := make([]*runState, 0, len(s.runs))
	runs := make([]Run, 0, len(s.runs))
	for _, state := range s.runs {
		if state.cmd != nil && state.cmd.Process != nil &&
			(state.run.Status == RunStatusRunning || state.run.Status == RunStatusStarting) {
			state.stopping = true
			if state.run.PID == 0 {
				state.run.PID = state.cmd.Process.Pid
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
	cmd := s.startCmd(run.Cmd, run.CmdArgs...)
	configureRunCommand(cmd)
	cmd.Dir = run.Workspace
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = mergeEnv(os.Environ(), env)
	if err := cmd.Start(); err != nil {
		return err
	}
	state.cmd = cmd
	run.PID = cmd.Process.Pid

	for index := range run.Companions {
		process := &run.Companions[index]
		companionCmd := s.startCmd(process.Cmd, process.CmdArgs...)
		configureCompanionRunCommand(companionCmd, run.PID)
		companionCmd.Dir = run.Workspace
		companionCmd.Stdout = stdout
		companionCmd.Stderr = stderr
		companionCmd.Env = mergeEnv(os.Environ(), env)
		if err := companionCmd.Start(); err != nil {
			if !process.Required {
				process.Error = err.Error()
				continue
			}
			s.mu.Lock()
			state.mainExited = true
			s.mu.Unlock()
			_ = killRunProcess(run)
			_ = cmd.Wait()
			state.companionWG.Wait()
			return fmt.Errorf("start companion %s: %w", process.ID, err)
		}
		process.PID = companionCmd.Process.Pid
		process.Error = ""
		state.companionCmds = append(state.companionCmds, companionCmd)
		state.companionWG.Add(1)
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
// workspace, preserving the run ID and replacing the previous log files.
// The run's Cmd and CmdArgs must have been persisted when the run was
// originally started.
func (s *Store) Resume(opts ResumeOptions) (*Run, error) {
	s.mu.Lock()
	state := s.runs[opts.ID]
	s.mu.Unlock()
	if state == nil {
		return nil, errors.New("run not found")
	}
	run := state.run
	if run.Status == RunStatusRunning || run.Status == RunStatusStarting {
		return nil, errors.New("run is already active")
	}
	if run.WorkspaceCleaned {
		return nil, errors.New("workspace has been cleaned up")
	}
	if run.Cmd == "" {
		return nil, errors.New("run has no persisted command")
	}
	alive, matches := runProcessStatus(run)
	if alive {
		if !matches {
			return nil, fmt.Errorf("run pid %d is still alive but does not belong to the saved run workspace", run.PID)
		}
		if err := killRunProcess(run); err != nil {
			return nil, err
		}
		if !waitForRunProcessExit(run, 2*time.Second) {
			return nil, fmt.Errorf("run pid %d is still alive", run.PID)
		}
	}

	device := node.DeviceInfo{Serial: run.Serial, NodeID: run.NodeID}
	if devices, err := s.devices.ListDevices(); err == nil {
		for _, candidate := range devices {
			if candidate.Serial == run.Serial {
				device = candidate
				break
			}
		}
	}

	p := s.programForRun(run)
	variables := mergeVariables(run.Env, opts.Variables)
	if p.ConfigFile != "" {
		configPath := filepath.Join(run.Workspace, p.ConfigFile)
		templatePath := configTemplatePath(run.Workspace, p.ConfigFile)
		if !fileExists(templatePath) {
			bundleConfigPath := filepath.Join(s.bundlePath(run.ProgramID), p.ConfigFile)
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
		if err := applyConfigReplacements(configPath, p.ConfigMappings, variables, device); err != nil {
			return nil, err
		}
	}

	// Start a fresh log stream for the resumed attempt.
	run.StdoutLogStart = 0
	run.StderrLogStart = 0
	stdout, err := s.newRunLogWriter(run, filepath.Join(run.Workspace, "stdout.log"), "stdout")
	if err != nil {
		return nil, err
	}
	stderr, err := s.newRunLogWriter(run, filepath.Join(run.Workspace, "stderr.log"), "stderr")
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}

	env := withDefaultRunEnv(variables)

	s.mu.Lock()
	run.Status = RunStatusStarting
	run.AutostartPaused = false
	run.StopRequestedAt = nil
	run.StopAcknowledgedAt = nil
	run.ExitCode = nil
	run.Error = ""
	run.CompletedAt = nil
	run.PID = 0
	for index := range run.Companions {
		run.Companions[index].PID = 0
		run.Companions[index].Error = ""
	}
	run.StartedAt = time.Now().UTC()
	state.stopping = false
	state.mainExited = false
	state.companionFailure = ""
	state.companionCmds = nil
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

func (s *Store) startOne(p Program, device node.DeviceInfo, nodes []node.NodeInfo, variables map[string]string) (*Run, error) {
	id := uuid.NewString()
	workspace := filepath.Join(s.instanceDir(), id)
	if err := copyDir(s.bundlePath(p.ID), workspace); err != nil {
		return nil, err
	}
	runVariables := buildRunVariables(p.ConfigMappings, variables, device)
	if p.ConfigFile != "" {
		configPath := filepath.Join(workspace, p.ConfigFile)
		templatePath := configTemplatePath(workspace, p.ConfigFile)
		if err := os.MkdirAll(filepath.Dir(templatePath), 0700); err != nil {
			return nil, err
		}
		if err := copyFile(configPath, templatePath, 0600); err != nil {
			return nil, err
		}
		if err := applyConfigReplacements(configPath, p.ConfigMappings, runVariables, device); err != nil {
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
	env["MAST_RUN_ID"] = id

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
	state.run.PID = 0
	for index := range state.run.Companions {
		state.run.Companions[index].PID = 0
	}
	if state.stopping {
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
		if run.SchemaVersion == 0 {
			run.SchemaVersion = runSchemaVersion
		}
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
