package program

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brijorn/mast/internal/node"
	"github.com/google/uuid"
)

const (
	RegistryFileName = "registry.json"
	DefaultADBPort   = 5037
	runLogMaxBytes   = 10 << 20

	RunStatusStarting = "starting"
	RunStatusRunning  = "running"
	RunStatusExited   = "exited"
	RunStatusFailed   = "failed"
	RunStatusStopped  = "stopped"
	RunStatusLost     = "lost"
)

type Entry struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type ConfigMapping struct {
	Section string `json:"section,omitempty"`
	Key     string `json:"key,omitempty"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

type Program struct {
	ID             string          `json:"id"`
	Slug           string          `json:"slug,omitempty"`
	Version        int             `json:"version"`
	Name           string          `json:"name"`
	ConfigFile     string          `json:"config_file,omitempty"`
	ConfigMappings []ConfigMapping `json:"config_mappings,omitempty"`
	Entry          Entry           `json:"entry"`
	CreatedAt      time.Time       `json:"created_at"`
}

type Run struct {
	ID             string            `json:"id"`
	ProgramID      string            `json:"program_id"`
	ProgramSlug    string            `json:"program_slug,omitempty"`
	ProgramVersion int               `json:"program_version,omitempty"`
	Serial         string            `json:"serial"`
	NodeID         string            `json:"node_id"`
	Workspace      string            `json:"workspace"`
	Status         string            `json:"status"`
	Autostart      bool              `json:"autostart,omitempty"`
	ExitCode       *int              `json:"exit_code,omitempty"`
	Error          string            `json:"error,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	// Cmd and CmdArgs are the resolved command and arguments used to start this
	// run. They are persisted so that Resume can re-execute the same process.
	Cmd         string     `json:"cmd,omitempty"`
	CmdArgs     []string   `json:"cmd_args,omitempty"`
	PID         int        `json:"pid,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// WorkspaceCleaned is true after the run's workspace directory has been
	// removed. Set by CleanupRun or auto-cleanup on next start for the serial.
	WorkspaceCleaned bool  `json:"workspace_cleaned,omitempty"`
	StdoutLogStart   int64 `json:"stdout_log_start,omitempty"`
	StderrLogStart   int64 `json:"stderr_log_start,omitempty"`
}

// UploadFile is a single file within a directory upload.
// Path is the relative path inside the program bundle (e.g. "config.ini").
type UploadFile struct {
	Path    string
	Content io.Reader
}

// RegisterUploadOptions describes a program bundle uploaded as individual files.
type RegisterUploadOptions struct {
	Name           string
	ConfigFile     string
	ConfigMappings []ConfigMapping
	Entry          Entry
	Files          []UploadFile
}

type StartOptions struct {
	ProgramID string            `json:"program_id"`
	Serials   []string          `json:"serials"`
	Variables map[string]string `json:"variables,omitempty"`
}

type LogOffsets struct {
	Stdout int64
	Stderr int64
}

type LogsResult struct {
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	StdoutOffset int64  `json:"stdout_offset"`
	StderrOffset int64  `json:"stderr_offset"`
	StdoutSize   int64  `json:"stdout_size"`
	StderrSize   int64  `json:"stderr_size"`
	StdoutReset  bool   `json:"stdout_reset,omitempty"`
	StderrReset  bool   `json:"stderr_reset,omitempty"`
}

type Store struct {
	root     string
	mu       sync.Mutex
	programs map[string]Program
	runs     map[string]*runState
	devices  deviceLister
	startCmd func(command string, args ...string) *exec.Cmd
	runners  map[string]string
}

type deviceLister interface {
	ListDevices() ([]node.DeviceInfo, error)
	ListNodes() []node.NodeInfo
}

type runState struct {
	run      *Run
	cmd      *exec.Cmd
	stopping bool
}

type registryFile struct {
	Programs []Program `json:"programs"`
}

func NewStore(root string, devices deviceLister) (*Store, error) {
	if root == "" {
		return nil, errors.New("program root required")
	}

	s := &Store{
		root:     root,
		programs: make(map[string]Program),
		runs:     make(map[string]*runState),
		devices:  devices,
		startCmd: exec.Command,
	}
	if err := os.MkdirAll(s.bundleDir(), 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.instanceDir(), 0700); err != nil {
		return nil, err
	}
	if err := s.loadRegistry(); err != nil {
		return nil, err
	}
	// Restore run history from workspace directories. Runs that were still
	// running or starting when the daemon stopped are marked as lost because
	// Mast no longer owns a process handle for them.
	s.loadRuns()
	go s.resumeAutostartRuns()
	return s, nil
}

func (s *Store) Root() string {
	return s.root
}

// RegisterUpload registers a program from a set of uploaded files. Files are
// written directly into a temporary directory inside the bundle store, then
// atomically moved to the final content-addressed path.
//
// Re-uploading a program with the same slug replaces the current bundle and
// increments the program version. Running instances are not affected because
// they execute from a copied workspace.
func (s *Store) RegisterUpload(opts RegisterUploadOptions) (*Program, error) {
	if opts.Entry.Command == "" {
		return nil, errors.New("entry command required")
	}
	if len(opts.Files) == 0 {
		return nil, errors.New("at least one file required")
	}

	// Create a temporary directory inside bundleDir so that os.Rename later
	// stays on the same filesystem (avoiding a cross-device link error).
	tmp, err := os.MkdirTemp(s.bundleDir(), "upload-*")
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(tmp)
		}
	}()

	for _, f := range opts.Files {
		rel := filepath.FromSlash(f.Path)
		if strings.Contains(rel, "..") {
			return nil, fmt.Errorf("invalid file path: %q", f.Path)
		}
		target := filepath.Join(tmp, rel)
		// Guard against path traversal after joining.
		if !strings.HasPrefix(
			filepath.Clean(target)+string(os.PathSeparator),
			filepath.Clean(tmp)+string(os.PathSeparator),
		) {
			return nil, fmt.Errorf("invalid file path: %q", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return nil, err
		}
		_, copyErr := io.Copy(out, f.Content)
		_ = out.Close()
		if copyErr != nil {
			return nil, copyErr
		}
	}

	id, err := hashDir(tmp)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = "unnamed"
	}
	slug := toSlug(name)
	s.mu.Lock()
	previous, hasPrevious := s.programBySlugLocked(slug)
	s.mu.Unlock()
	version := 1
	if hasPrevious {
		version = previous.Version + 1
	}
	program := Program{
		ID:             id,
		Slug:           slug,
		Version:        version,
		Name:           name,
		ConfigFile:     opts.ConfigFile,
		ConfigMappings: opts.ConfigMappings,
		Entry:          opts.Entry,
		CreatedAt:      time.Now().UTC(),
	}

	bundlePath := s.bundlePath(id)
	// Remove the target path if it already exists (same content = idempotent).
	if err := os.RemoveAll(bundlePath); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, bundlePath); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(bundlePath, "mast-program.json"), program); err != nil {
		return nil, err
	}
	success = true

	var previousID string
	s.mu.Lock()
	if hasPrevious {
		previousID = previous.ID
		delete(s.programs, previousID)
	}
	s.programs[id] = program
	err = s.saveRegistryLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if previousID != "" && previousID != id {
		_ = os.RemoveAll(s.bundlePath(previousID))
	}

	return &program, nil
}

func (s *Store) UpdateProgram(id string, mappings []ConfigMapping) (*Program, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.programs[id]
	if !ok {
		return nil, errors.New("program not found")
	}

	p.ConfigMappings = mappings
	s.programs[id] = p

	if err := s.saveRegistryLocked(); err != nil {
		return nil, err
	}

	return &p, nil
}

func (s *Store) DeleteProgram(id string) error {
	s.mu.Lock()
	p, ok := s.programs[id]
	if !ok {
		p, ok = s.programBySlugLocked(id)
	}
	if !ok {
		s.mu.Unlock()
		return errors.New("program not found")
	}

	delete(s.programs, p.ID)
	if err := s.saveRegistryLocked(); err != nil {
		s.programs[p.ID] = p
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	return os.RemoveAll(s.bundlePath(p.ID))
}

func (s *Store) ListPrograms() []Program {
	s.mu.Lock()
	defer s.mu.Unlock()

	programs := make([]Program, 0, len(s.programs))
	for _, p := range s.programs {
		programs = append(programs, p)
	}
	sort.Slice(programs, func(i, j int) bool {
		return programs[i].CreatedAt.Before(programs[j].CreatedAt)
	})
	return programs
}

func (s *Store) programBySlugLocked(slug string) (Program, bool) {
	for _, p := range s.programs {
		if p.Slug == slug {
			return p, true
		}
	}
	return Program{}, false
}

func (s *Store) ListRuns() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()

	runs := make([]Run, 0, len(s.runs))
	for _, state := range s.runs {
		run := *state.run
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.Before(runs[j].StartedAt)
	})
	return runs
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

	devices, err := s.devices.ListDevices()
	if err != nil {
		return nil, err
	}
	nodes := s.devices.ListNodes()

	var runs []Run
	for _, serial := range opts.Serials {
		device, ok := findDevice(devices, serial)
		if !ok {
			return nil, fmt.Errorf("device not found: %s", serial)
		}

		run, err := s.startOne(p, device, nodes, opts.Variables)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, nil
}

func (s *Store) Stop(id string) (*Run, error) {
	s.mu.Lock()
	state := s.runs[id]
	if state == nil {
		s.mu.Unlock()
		return nil, errors.New("run not found")
	}
	state.run.Autostart = false
	if state.cmd == nil || state.cmd.Process == nil {
		run := *state.run
		s.mu.Unlock()
		_ = writeJSON(filepath.Join(run.Workspace, "run.json"), &run)
		return &run, nil
	}
	state.stopping = true
	if state.run.PID == 0 {
		state.run.PID = state.cmd.Process.Pid
	}
	run := *state.run
	s.mu.Unlock()
	_ = writeJSON(filepath.Join(run.Workspace, "run.json"), &run)
	if err := killRunProcess(&run); err != nil {
		return nil, err
	}
	return &run, nil
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
	}
	state.run.Autostart = enabled
	run := *state.run
	s.mu.Unlock()

	if err := writeJSON(filepath.Join(run.Workspace, "run.json"), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) Shutdown() {
	s.mu.Lock()
	states := make([]*runState, 0, len(s.runs))
	for _, state := range s.runs {
		if state.cmd != nil && state.cmd.Process != nil &&
			(state.run.Status == RunStatusRunning || state.run.Status == RunStatusStarting) {
			state.stopping = true
			if state.run.PID == 0 {
				state.run.PID = state.cmd.Process.Pid
			}
			states = append(states, state)
		}
	}
	s.mu.Unlock()

	for _, state := range states {
		_ = killRunProcess(state.run)
	}
	for _, state := range states {
		_ = waitForRunProcessExit(state.run, 2*time.Second)
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

// Resume re-executes a completed, failed, stopped, or lost run in its existing
// workspace, preserving the run ID and replacing the previous log files.
// The run's Cmd and CmdArgs must have been persisted when the run was
// originally started.
func (s *Store) Resume(id string) (*Run, error) {
	s.mu.Lock()
	state := s.runs[id]
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

	cmd := s.startCmd(run.Cmd, run.CmdArgs...)
	configureRunCommand(cmd)
	cmd.Dir = run.Workspace
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	env := withDefaultRunEnv(run.Env)
	cmd.Env = mergeEnv(os.Environ(), env)

	s.mu.Lock()
	run.Status = RunStatusStarting
	run.ExitCode = nil
	run.Error = ""
	run.CompletedAt = nil
	run.PID = 0
	run.StartedAt = time.Now().UTC()
	run.Env = env
	state.stopping = false
	s.mu.Unlock()

	_ = writeJSON(filepath.Join(run.Workspace, "run.json"), run)

	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		s.mu.Lock()
		run.Status = RunStatusFailed
		run.Error = err.Error()
		now := time.Now().UTC()
		run.CompletedAt = &now
		_ = writeJSON(filepath.Join(run.Workspace, "run.json"), run)
		s.mu.Unlock()
		return nil, err
	}

	s.mu.Lock()
	run.Status = RunStatusRunning
	run.PID = cmd.Process.Pid
	state.cmd = cmd
	_ = writeJSON(filepath.Join(run.Workspace, "run.json"), run)
	s.mu.Unlock()

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

func (s *Store) Logs(id string) (string, string, error) {
	logs, err := s.LogsSince(id, LogOffsets{})
	if err != nil {
		return "", "", err
	}
	return logs.Stdout, logs.Stderr, nil
}

func (s *Store) LogsSince(id string, offsets LogOffsets) (*LogsResult, error) {
	s.mu.Lock()
	state := s.runs[id]
	s.mu.Unlock()
	if state == nil {
		return nil, errors.New("run not found")
	}

	stdout, stdoutOffset, stdoutSize, stdoutReset, err := readLogFileSince(filepath.Join(state.run.Workspace, "stdout.log"), offsets.Stdout, state.run.StdoutLogStart)
	if err != nil {
		return nil, err
	}
	stderr, stderrOffset, stderrSize, stderrReset, err := readLogFileSince(filepath.Join(state.run.Workspace, "stderr.log"), offsets.Stderr, state.run.StderrLogStart)
	if err != nil {
		return nil, err
	}
	return &LogsResult{
		Stdout:       stdout,
		Stderr:       stderr,
		StdoutOffset: stdoutOffset,
		StderrOffset: stderrOffset,
		StdoutSize:   stdoutSize,
		StderrSize:   stderrSize,
		StdoutReset:  stdoutReset,
		StderrReset:  stderrReset,
	}, nil
}

// cleanupCompletedRunsForSerial removes workspace directories for all
// completed or failed runs belonging to the given device serial. It is called
// automatically before a new run is started on that serial so that disk space
// from prior runs is reclaimed when the phone switches programs.
func (s *Store) cleanupCompletedRunsForSerial(serial string) {
	s.mu.Lock()
	var toClean []*runState
	for _, state := range s.runs {
		if state.run.Serial == serial &&
			(state.run.Status == RunStatusExited || state.run.Status == RunStatusFailed || state.run.Status == RunStatusStopped) &&
			!state.run.WorkspaceCleaned {
			toClean = append(toClean, state)
		}
	}
	s.mu.Unlock()

	for _, state := range toClean {
		if err := os.RemoveAll(state.run.Workspace); err == nil || os.IsNotExist(err) {
			s.mu.Lock()
			state.run.WorkspaceCleaned = true
			s.mu.Unlock()
		}
	}
}

func (s *Store) startOne(p Program, device node.DeviceInfo, nodes []node.NodeInfo, variables map[string]string) (*Run, error) {
	// Reclaim disk space from prior completed/failed runs on this serial
	// before creating the new workspace.
	s.cleanupCompletedRunsForSerial(device.Serial)

	id := uuid.NewString()
	workspace := filepath.Join(s.instanceDir(), id)
	if err := copyDir(s.bundlePath(p.ID), workspace); err != nil {
		return nil, err
	}
	if p.ConfigFile != "" {
		if err := applyConfigReplacements(filepath.Join(workspace, p.ConfigFile), p.ConfigMappings, variables, device); err != nil {
			return nil, err
		}
	}

	env := defaultRunEnv()
	for key, value := range adbEnv(device, nodes) {
		env[key] = value
	}
	for key, value := range variables {
		env[key] = value
	}

	command := p.Entry.Command
	resolvedArgs := make([]string, len(p.Entry.Args))
	for i, arg := range p.Entry.Args {
		resolvedArgs[i] = resolveValue(arg, variables, device)
	}
	if localCommand := filepath.Join(workspace, command); fileExists(localCommand) {
		command = localCommand
	}
	command, args, err := s.runnerCommand(command, resolvedArgs)
	if err != nil {
		return nil, err
	}

	run := &Run{
		ID:             id,
		ProgramID:      p.ID,
		ProgramSlug:    p.Slug,
		ProgramVersion: p.Version,
		Serial:         device.Serial,
		NodeID:         device.NodeID,
		Workspace:      workspace,
		Status:         RunStatusStarting,
		Env:            env,
		Cmd:            command,
		CmdArgs:        args,
		StartedAt:      time.Now().UTC(),
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
	cmd := s.startCmd(command, args...)
	configureRunCommand(cmd)
	cmd.Dir = workspace
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = mergeEnv(os.Environ(), env)
	if err := writeJSON(filepath.Join(workspace, "run.json"), run); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}

	run.Status = RunStatusRunning
	run.PID = cmd.Process.Pid
	state := &runState{run: run, cmd: cmd}
	_ = writeJSON(filepath.Join(workspace, "run.json"), run)
	s.mu.Lock()
	s.runs[id] = state
	s.mu.Unlock()

	go s.waitRun(state, stdout, stderr)
	return run, nil
}

func (s *Store) waitRun(state *runState, stdout, stderr io.Closer) {
	err := state.cmd.Wait()
	_ = stdout.Close()
	_ = stderr.Close()

	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	state.run.CompletedAt = &now
	state.run.PID = 0
	if state.stopping {
		state.run.ExitCode = nil
		state.run.Status = RunStatusStopped
		state.run.Error = ""
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
	_ = writeJSON(filepath.Join(state.run.Workspace, "run.json"), state.run)
}

func (s *Store) bundleDir() string {
	return filepath.Join(s.root, "bundles")
}

func (s *Store) instanceDir() string {
	return filepath.Join(s.root, "instances")
}

func (s *Store) bundlePath(id string) string {
	return filepath.Join(s.bundleDir(), id)
}

func (s *Store) registryPath() string {
	return filepath.Join(s.root, RegistryFileName)
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
			_ = writeJSON(runFile, &run)
		}
		s.runs[run.ID] = &runState{run: &run}
	}
}

func (s *Store) resumeAutostartRuns() {
	s.mu.Lock()
	ids := make([]string, 0)
	for id, state := range s.runs {
		run := state.run
		if !run.Autostart || run.WorkspaceCleaned || run.Cmd == "" {
			continue
		}
		if run.Status == RunStatusStopped || run.Status == RunStatusLost {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()

	for _, id := range ids {
		if _, err := s.Resume(id); err != nil {
			s.mu.Lock()
			state := s.runs[id]
			if state != nil {
				state.run.Error = "autostart resume failed: " + err.Error()
				_ = writeJSON(filepath.Join(state.run.Workspace, "run.json"), state.run)
			}
			s.mu.Unlock()
		}
	}
}

func (s *Store) loadRegistry() error {
	f, err := os.Open(s.registryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	var registry registryFile
	if err := json.NewDecoder(f).Decode(&registry); err != nil {
		return err
	}
	for _, p := range registry.Programs {
		s.programs[p.ID] = p
	}
	return nil
}

func (s *Store) saveRegistryLocked() error {
	programs := make([]Program, 0, len(s.programs))
	for _, p := range s.programs {
		programs = append(programs, p)
	}
	sort.Slice(programs, func(i, j int) bool {
		return programs[i].CreatedAt.Before(programs[j].CreatedAt)
	})
	return writeJSON(s.registryPath(), registryFile{Programs: programs})
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func hashDir(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(h, filepath.ToSlash(rel)+"\n"); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() {
			_ = f.Close()
		}()
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "", err
	}
	return "sha256-" + hex.EncodeToString(h.Sum(nil)), nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		_ = in.Close()
	}()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()
	_, err = io.Copy(out, in)
	return err
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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
	if runners != nil {
		ext := filepath.Ext(command)
		if r, ok := runners[ext]; ok && r != "" {
			runner = r
		}
	}

	if runner != "" {
		parts := strings.Fields(runner)
		if len(parts) > 0 {
			return parts[0], append(append(parts[1:], command), args...), nil
		}
	}

	if filepath.Ext(command) == ".exe" && runtime.GOOS != "windows" {
		return "", nil, fmt.Errorf("no runner configured for non-native executable %q", command)
	}
	return command, args, nil
}

func findDevice(devices []node.DeviceInfo, serial string) (node.DeviceInfo, bool) {
	for _, device := range devices {
		if device.Serial == serial {
			return device, true
		}
	}
	return node.DeviceInfo{}, false
}

func adbEnv(device node.DeviceInfo, nodes []node.NodeInfo) map[string]string {
	env := map[string]string{
		"ANDROID_SERIAL": device.Serial,
	}
	for _, n := range nodes {
		if n.ID != device.NodeID || n.Local {
			continue
		}
		host, _ := splitHostPortDefault(n.Addr, DefaultADBPort)
		if host == "" {
			continue
		}
		port := n.ADBPort
		if port <= 0 {
			port = DefaultADBPort
		}
		env["ADB_SERVER_SOCKET"] = fmt.Sprintf("tcp:%s:%d", host, port)
		env["ANDROID_ADB_SERVER_ADDRESS"] = host
		env["ANDROID_ADB_SERVER_HOST"] = host
		env["ANDROID_ADB_SERVER_PORT"] = strconv.Itoa(port)
	}
	return env
}

func splitHostPortDefault(addr string, defaultPort int) (string, int) {
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimSuffix(addr, "/")
	host, portText, ok := strings.Cut(addr, ":")
	if !ok {
		return addr, defaultPort
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return host, defaultPort
	}
	return host, port
}

func mergeEnv(base []string, overlay map[string]string) []string {
	index := make(map[string]int)
	env := append([]string(nil), base...)
	for i, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			index[key] = i
		}
	}
	for key, value := range overlay {
		item := key + "=" + value
		if i, ok := index[key]; ok {
			env[i] = item
		} else {
			env = append(env, item)
		}
	}
	return env
}

type boundedLogWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	start    int64
	size     int64
	file     *os.File
	onTrim   func(start int64)
}

func (s *Store) newRunLogWriter(run *Run, path, stream string) (*boundedLogWriter, error) {
	if err := removeLogFiles(path); err != nil {
		return nil, err
	}
	return newBoundedLogWriter(path, runLogMaxBytes, func(start int64) {
		s.mu.Lock()
		switch stream {
		case "stdout":
			run.StdoutLogStart = start
		case "stderr":
			run.StderrLogStart = start
		}
		_ = writeJSON(filepath.Join(run.Workspace, "run.json"), run)
		s.mu.Unlock()
	})
}

func newBoundedLogWriter(path string, maxBytes int64, onTrim func(start int64)) (*boundedLogWriter, error) {
	if maxBytes <= 0 {
		maxBytes = runLogMaxBytes
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	return &boundedLogWriter{
		path:     path,
		maxBytes: maxBytes,
		file:     file,
		onTrim:   onTrim,
	}, nil
}

func (w *boundedLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	if err := w.trimLocked(); err != nil {
		return n, err
	}
	return n, nil
}

func (w *boundedLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *boundedLogWriter) trimLocked() error {
	if w.size <= w.maxBytes {
		return nil
	}
	trimBytes := w.size - w.maxBytes
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	in, err := os.Open(w.path)
	if err != nil {
		return err
	}
	if _, err := in.Seek(trimBytes, io.SeekStart); err != nil {
		_ = in.Close()
		return err
	}
	tail, err := io.ReadAll(in)
	_ = in.Close()
	if err != nil {
		return err
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, tail, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, w.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	w.file = file
	w.start += trimBytes
	w.size = int64(len(tail))
	if w.onTrim != nil {
		w.onTrim(w.start)
	}
	return nil
}

func readLogFileSince(path string, offset, start int64) (string, int64, int64, bool, error) {
	if offset < 0 {
		offset = 0
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", start, 0, offset > start, nil
		}
		return "", 0, 0, false, err
	}
	defer func() {
		_ = file.Close()
	}()

	info, err := file.Stat()
	if err != nil {
		return "", 0, 0, false, err
	}
	size := info.Size()
	end := start + size
	reset := false
	if offset < start || offset > end {
		offset = start
		reset = true
	}
	if _, err := file.Seek(offset-start, io.SeekStart); err != nil {
		return "", 0, 0, false, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", 0, 0, false, err
	}
	return string(data), end, size, reset, nil
}

func removeLogFiles(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(path + ".tmp"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func defaultRunEnv() map[string]string {
	return map[string]string{
		"PYTHONUNBUFFERED": "1",
	}
}

func withDefaultRunEnv(overrides map[string]string) map[string]string {
	env := defaultRunEnv()
	for key, value := range overrides {
		env[key] = value
	}
	return env
}

func applyConfigReplacements(path string, values []ConfigMapping, variables map[string]string, device node.DeviceInfo) error {
	if len(values) == 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)

	// 1. Global placeholder replace (e.g., replacing "{{license_key}}" inside config.py)
	for _, val := range values {
		var placeholders []string
		if val.Key != "" {
			placeholders = append(placeholders, "{{"+val.Key+"}}", "{{"+strings.ToLower(val.Key)+"}}")
		}
		if strings.HasPrefix(val.Value, "{{") && strings.HasSuffix(val.Value, "}}") {
			placeholders = append(placeholders, val.Value)
		}

		if len(placeholders) == 0 {
			continue
		}

		resolvedVal := val.Value
		varKey := val.Key
		if varKey == "" && strings.HasPrefix(val.Value, "{{") && strings.HasSuffix(val.Value, "}}") {
			varKey = strings.TrimSuffix(strings.TrimPrefix(val.Value, "{{"), "}}")
		}

		if varKey != "" {
			if v, ok := variables[varKey]; ok && v != "" {
				resolvedVal = v
			} else if v, ok := variables[strings.ToLower(varKey)]; ok && v != "" {
				resolvedVal = v
			}
		}

		resolved := resolveValue(resolvedVal, variables, device)
		for _, ph := range placeholders {
			content = strings.ReplaceAll(content, ph, resolved)
		}
	}

	// 2. Structured INI replacement (fallback for traditional .ini config files)
	if filepath.Ext(path) == ".ini" {
		content = renderINIValues(content, values, variables, device)
	}

	return os.WriteFile(path, []byte(content), 0600)
}

func renderINIValues(input string, values []ConfigMapping, variables map[string]string, device node.DeviceInfo) string {
	type sectionKey struct {
		section string
		key     string
	}
	replacements := make(map[sectionKey]string)
	for _, value := range values {
		resolvedVal := value.Value
		if v, ok := variables[value.Key]; ok && v != "" {
			resolvedVal = v
		} else if v, ok := variables[strings.ToLower(value.Key)]; ok && v != "" {
			resolvedVal = v
		}
		replacements[sectionKey{section: strings.ToLower(value.Section), key: strings.ToLower(value.Key)}] = resolveValue(resolvedVal, variables, device)
	}

	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(input))
	section := ""
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
			end := strings.Index(trimmed, "]")
			section = strings.ToLower(strings.TrimSpace(trimmed[1:end]))
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		replacement, ok := replacements[sectionKey{section: section, key: strings.ToLower(strings.TrimSpace(key))}]
		if !ok {
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		prefix := line[:strings.Index(line, "=")+1]
		out.WriteString(prefix)
		out.WriteString(" ")
		out.WriteString(replacement)
		out.WriteString("\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func resolveValue(value string, variables map[string]string, device node.DeviceInfo) string {
	current := value
	for i := 0; i < 5; i++ {
		next := replaceOnce(current, variables, device)
		if next == current {
			break
		}
		current = next
	}
	return current
}

func replaceOnce(val string, variables map[string]string, device node.DeviceInfo) string {
	var out strings.Builder
	pos := 0
	for {
		start := strings.Index(val[pos:], "{{")
		if start == -1 {
			out.WriteString(val[pos:])
			break
		}
		startIdx := pos + start
		end := strings.Index(val[startIdx:], "}}")
		if end == -1 {
			out.WriteString(val[pos:])
			break
		}
		endIdx := startIdx + end

		// Write prefix
		out.WriteString(val[pos:startIdx])

		// Extract placeholder name
		placeholder := strings.TrimSpace(val[startIdx+2 : endIdx])

		// Resolve placeholder
		var resolved string
		switch placeholder {
		case "phone.serial":
			resolved = device.Serial
		case "phone.node_id":
			resolved = device.NodeID
		default:
			if v, ok := variables[placeholder]; ok {
				resolved = v
			} else {
				// Keep the placeholder as is if not resolved
				resolved = val[startIdx : endIdx+2]
			}
		}

		out.WriteString(resolved)
		pos = endIdx + 2
	}
	return out.String()
}
