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
)

type Entry struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type INIValue struct {
	Section string `json:"section"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

type Program struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Platform  string     `json:"platform"`
	Entry     Entry      `json:"entry"`
	INIValues []INIValue `json:"ini_values,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type Run struct {
	ID          string            `json:"id"`
	ProgramID   string            `json:"program_id"`
	Serial      string            `json:"serial"`
	NodeID      string            `json:"node_id"`
	Workspace   string            `json:"workspace"`
	Status      string            `json:"status"`
	ExitCode    *int              `json:"exit_code,omitempty"`
	Error       string            `json:"error,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
}

type RegisterOptions struct {
	Path      string     `json:"path"`
	Name      string     `json:"name,omitempty"`
	Platform  string     `json:"platform,omitempty"`
	Entry     Entry      `json:"entry"`
	INIValues []INIValue `json:"ini_values,omitempty"`
}

type StartOptions struct {
	ProgramID string            `json:"program_id"`
	Serials   []string          `json:"serials"`
	Variables map[string]string `json:"variables,omitempty"`
}

type Store struct {
	root     string
	mu       sync.Mutex
	programs map[string]Program
	runs     map[string]*runState
	devices  deviceLister
	startCmd func(command string, args ...string) *exec.Cmd
}

type deviceLister interface {
	ListDevices() ([]node.DeviceInfo, error)
	ListNodes() []node.NodeInfo
}

type runState struct {
	run *Run
	cmd *exec.Cmd
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
	return s, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) Register(opts RegisterOptions) (*Program, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("path required")
	}
	if opts.Entry.Command == "" {
		return nil, errors.New("entry command required")
	}

	info, err := os.Stat(opts.Path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("path must be a directory")
	}

	id, err := hashDir(opts.Path)
	if err != nil {
		return nil, err
	}

	name := opts.Name
	if name == "" {
		name = filepath.Base(opts.Path)
	}
	platform := opts.Platform
	if platform == "" {
		platform = inferPlatform(opts.Entry.Command)
	}

	program := Program{
		ID:        id,
		Name:      name,
		Platform:  platform,
		Entry:     opts.Entry,
		INIValues: opts.INIValues,
		CreatedAt: time.Now().UTC(),
	}

	bundlePath := s.bundlePath(id)
	if err := os.RemoveAll(bundlePath); err != nil {
		return nil, err
	}
	if err := copyDir(opts.Path, bundlePath); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(bundlePath, "mast-program.json"), program); err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.programs[id] = program
	err = s.saveRegistryLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}

	return &program, nil
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

func (s *Store) ListRuns() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()

	runs := make([]Run, 0, len(s.runs))
	for _, state := range s.runs {
		runs = append(runs, *state.run)
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
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("program not found")
	}
	if err := checkPlatform(p.Platform); err != nil {
		return nil, err
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
	s.mu.Unlock()
	if state == nil {
		return nil, errors.New("run not found")
	}
	if state.cmd == nil || state.cmd.Process == nil {
		return state.run, nil
	}
	if err := state.cmd.Process.Kill(); err != nil {
		return nil, err
	}
	return state.run, nil
}

func (s *Store) Logs(id string) (string, string, error) {
	s.mu.Lock()
	state := s.runs[id]
	s.mu.Unlock()
	if state == nil {
		return "", "", errors.New("run not found")
	}

	stdout, err := os.ReadFile(filepath.Join(state.run.Workspace, "stdout.log"))
	if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	stderr, err := os.ReadFile(filepath.Join(state.run.Workspace, "stderr.log"))
	if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	return string(stdout), string(stderr), nil
}

func (s *Store) startOne(p Program, device node.DeviceInfo, nodes []node.NodeInfo, variables map[string]string) (*Run, error) {
	id := uuid.NewString()
	workspace := filepath.Join(s.instanceDir(), id)
	if err := copyDir(s.bundlePath(p.ID), workspace); err != nil {
		return nil, err
	}
	if err := applyINIValues(filepath.Join(workspace, "config.ini"), p.INIValues, variables, device); err != nil {
		return nil, err
	}

	env := adbEnv(device, nodes)
	for key, value := range variables {
		env[key] = value
	}

	stdout, err := os.Create(filepath.Join(workspace, "stdout.log"))
	if err != nil {
		return nil, err
	}
	stderr, err := os.Create(filepath.Join(workspace, "stderr.log"))
	if err != nil {
		_ = stdout.Close()
		return nil, err
	}

	command := p.Entry.Command
	args := p.Entry.Args
	if localCommand := filepath.Join(workspace, command); fileExists(localCommand) {
		command = localCommand
	}
	command, args = runnerCommand(p.Platform, command, args)
	cmd := s.startCmd(command, args...)
	cmd.Dir = workspace
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = mergeEnv(os.Environ(), env)

	run := &Run{
		ID:        id,
		ProgramID: p.ID,
		Serial:    device.Serial,
		NodeID:    device.NodeID,
		Workspace: workspace,
		Status:    "starting",
		Env:       env,
		StartedAt: time.Now().UTC(),
	}
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

	run.Status = "running"
	state := &runState{run: run, cmd: cmd}
	s.mu.Lock()
	s.runs[id] = state
	s.mu.Unlock()

	go s.waitRun(state, stdout, stderr)
	return run, nil
}

func (s *Store) waitRun(state *runState, stdout, stderr *os.File) {
	err := state.cmd.Wait()
	_ = stdout.Close()
	_ = stderr.Close()

	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	state.run.CompletedAt = &now
	if err == nil {
		code := 0
		state.run.ExitCode = &code
		state.run.Status = "exited"
	} else if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		state.run.ExitCode = &code
		state.run.Status = "failed"
		state.run.Error = err.Error()
	} else {
		state.run.Status = "failed"
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

func inferPlatform(command string) string {
	if strings.EqualFold(filepath.Ext(command), ".exe") {
		return "windows"
	}
	return runtime.GOOS
}

func checkPlatform(platform string) error {
	if platform == "" || platform == "any" || platform == runtime.GOOS {
		return nil
	}
	if platform == "windows" && runtime.GOOS == "linux" {
		if _, err := exec.LookPath("winerun"); err == nil {
			return nil
		}
		return errors.New("program platform \"windows\" requires winerun on linux")
	}
	return fmt.Errorf("program platform %q cannot run on %s", platform, runtime.GOOS)
}

func runnerCommand(platform string, command string, args []string) (string, []string) {
	if platform == "windows" && runtime.GOOS == "linux" {
		return "winerun", append([]string{command}, args...)
	}
	return command, args
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
		host := n.ADBHost
		port := n.ADBPort
		if host == "" {
			host, port = splitHostPortDefault(n.Addr, DefaultADBPort)
		}
		if host == "" {
			continue
		}
		if port <= 0 {
			port = DefaultADBPort
		}
		env["ADB_SERVER_SOCKET"] = fmt.Sprintf("tcp:%s:%d", host, port)
		env["ANDROID_ADB_SERVER_ADDRESS"] = host
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

func applyINIValues(path string, values []INIValue, variables map[string]string, device node.DeviceInfo) error {
	if len(values) == 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rendered := renderINIValues(string(data), values, variables, device)
	return os.WriteFile(path, []byte(rendered), 0600)
}

func renderINIValues(input string, values []INIValue, variables map[string]string, device node.DeviceInfo) string {
	type sectionKey struct {
		section string
		key     string
	}
	replacements := make(map[sectionKey]string)
	for _, value := range values {
		replacements[sectionKey{section: strings.ToLower(value.Section), key: strings.ToLower(value.Key)}] = resolveValue(value.Value, variables, device)
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
	switch value {
	case "{{phone.serial}}", "{{device.serial}}":
		return device.Serial
	case "{{phone.node_id}}", "{{device.node_id}}":
		return device.NodeID
	default:
		if strings.HasPrefix(value, "{{") && strings.HasSuffix(value, "}}") {
			key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "{{"), "}}"))
			if v, ok := variables[key]; ok {
				return v
			}
		}
		return value
	}
}
