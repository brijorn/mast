package program

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/node"
)

type fakeDevices struct {
	devices []node.DeviceInfo
	nodes   []node.NodeInfo
}

func (f fakeDevices) ListDevices() ([]node.DeviceInfo, error) {
	return f.devices, nil
}

func (f fakeDevices) ListNodes() []node.NodeInfo {
	return f.nodes
}

func registerTestProgram(t *testing.T, store *Store, source string, opts RegisterUploadOptions) (*Program, error) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		opts.Files = append(opts.Files, UploadFile{
			Path:    filepath.ToSlash(rel),
			Content: bytes.NewReader(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return store.RegisterUpload(opts)
}

func TestStartCopiesBundleRendersConfigAndSetsRemoteADBEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}

	config := `[Settings]
DEVICE_ID = old-device
RESOLUTION = 1080x2340
CELL_CONFIG = 1

[LICENSE]
LICENSE_KEY = YOUR-LICENSE-KEY
`
	if err := os.WriteFile(filepath.Join(source, "config.ini"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	script := `#!/bin/sh
cat config.ini
printf '\nSERIAL=%s\n' "$ANDROID_SERIAL"
printf 'SOCKET=%s\n' "$ADB_SERVER_SOCKET"
printf 'ADB_HOST=%s\n' "$ANDROID_ADB_SERVER_ADDRESS"
printf 'ADB_HOST_VAR=%s\n' "$ANDROID_ADB_SERVER_HOST"
printf 'ADB_PORT=%s\n' "$ANDROID_ADB_SERVER_PORT"
printf 'ARGS=%s\n' "$*"
`
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{
			{Serial: "remote-123", State: "device", NodeID: "peer-a"},
		},
		nodes: []node.NodeInfo{
			{ID: "local", Local: true, Addr: "127.0.0.1"},
			{ID: "peer-a", Local: false, Addr: "10.0.0.4:6271", ADBPort: 5038},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:       "test runner",
		ConfigFile: "config.ini",
		Entry:      Entry{Command: "/bin/sh", Args: []string{"run.sh", "--license", "{{license_key}}"}},
		ConfigMappings: []ConfigMapping{
			{Section: "Settings", Key: "DEVICE_ID", Value: "{{phone.serial}}"},
			{Section: "Settings", Key: "RESOLUTION", Value: "{{resolution}}"},
			{Section: "LICENSE", Key: "LICENSE_KEY", Value: "{{license_key}}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runs, err := store.Start(StartOptions{
		ProgramID: registered.ID,
		Serials:   []string{"remote-123"},
		Variables: map[string]string{
			"resolution":  "720x1600",
			"license_key": "abc-123",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}

	waitForRun(t, store, runs[0].ID)

	stdout, stderr, err := store.Logs(runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{
		"DEVICE_ID = remote-123",
		"RESOLUTION = 720x1600",
		"LICENSE_KEY = abc-123",
		"SERIAL=remote-123",
		"SOCKET=tcp:10.0.0.4:5038",
		"ADB_HOST=10.0.0.4",
		"ADB_HOST_VAR=10.0.0.4",
		"ADB_PORT=5038",
		"ARGS=--license abc-123",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func waitForRun(t *testing.T, store *Store, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, run := range store.ListRuns() {
			if run.ID == id && run.Status != "running" && run.Status != "starting" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", id)
}

func TestCustomRunners(t *testing.T) {
	s := &Store{
		runners: map[string]string{
			".exe": "wine",
			".py":  "python3 -u",
		},
	}

	// 1. Match by .exe extension
	cmd, args, err := s.runnerCommand("test.exe", []string{"arg1", "arg2"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "wine" {
		t.Errorf("expected cmd to be 'wine', got %q", cmd)
	}
	expectedArgs := []string{"test.exe", "arg1", "arg2"}
	if len(args) != len(expectedArgs) || args[0] != "test.exe" || args[1] != "arg1" || args[2] != "arg2" {
		t.Errorf("expected args to be %v, got %v", expectedArgs, args)
	}

	// 2. Match by .py file extension
	cmd, args, err = s.runnerCommand("test.py", []string{"arg1"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "python3" {
		t.Errorf("expected cmd to be 'python3', got %q", cmd)
	}
	expectedArgs = []string{"-u", "test.py", "arg1"}
	if len(args) != len(expectedArgs) || args[0] != "-u" || args[1] != "test.py" || args[2] != "arg1" {
		t.Errorf("expected args to be %v, got %v", expectedArgs, args)
	}

	// 3. Non-native executables require an explicit runner.
	s.SetRunners(nil)
	if runtime.GOOS != "windows" {
		_, _, err = s.runnerCommand("test.exe", []string{"arg1"})
		if err == nil {
			t.Fatal("expected no-runner error")
		}
	}
}

func TestRegisterUploadDeletesReplacedBundleDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.RegisterUpload(RegisterUploadOptions{
		Name:  "test app",
		Entry: Entry{Command: "run.sh"},
		Files: []UploadFile{
			{Path: "run.sh", Content: strings.NewReader("echo first\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 {
		t.Fatalf("first Version = %d, want 1", first.Version)
	}
	firstPath := store.bundlePath(first.ID)
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatal(err)
	}

	second, err := store.RegisterUpload(RegisterUploadOptions{
		Name:  "test app",
		Entry: Entry{Command: "run.sh"},
		Files: []UploadFile{
			{Path: "run.sh", Content: strings.NewReader("echo second\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 {
		t.Fatalf("second Version = %d, want 2", second.Version)
	}
	if first.ID == second.ID {
		t.Fatal("test setup produced identical bundle IDs")
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("old bundle stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(store.bundlePath(second.ID)); err != nil {
		t.Fatal(err)
	}
	programs := store.ListPrograms()
	if len(programs) != 1 || programs[0].ID != second.ID {
		t.Fatalf("programs = %+v, want only replacement bundle %s", programs, second.ID)
	}
	if programs[0].Version != 2 {
		t.Fatalf("registry Version = %d, want 2", programs[0].Version)
	}
}

func TestLoadRunsMarksActiveRunsLost(t *testing.T) {
	root := t.TempDir()
	programRoot := filepath.Join(root, "programs")
	instance := filepath.Join(programRoot, "instances", "run-1")
	if err := os.MkdirAll(instance, 0700); err != nil {
		t.Fatal(err)
	}
	run := Run{
		ID:        "run-1",
		ProgramID: "program-1",
		Serial:    "phone-1",
		NodeID:    "node-1",
		Workspace: instance,
		Status:    RunStatusRunning,
		Cmd:       "/bin/sh",
		CmdArgs:   []string{"run.sh"},
		PID:       999999,
		StartedAt: time.Now().UTC(),
	}
	if err := writeJSON(filepath.Join(instance, "run.json"), &run); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(programRoot, fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}

	runs := store.ListRuns()
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
	if runs[0].Status != RunStatusLost {
		t.Fatalf("Status = %q, want %q", runs[0].Status, RunStatusLost)
	}
	if runs[0].CompletedAt != nil {
		t.Fatalf("CompletedAt = %v, want nil", runs[0].CompletedAt)
	}
}

func TestResumeReusesRunIDAndWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\necho resumed\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "resume runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	before := findRun(t, store, started[0].ID)

	resumed, err := store.Resume(started[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != before.ID {
		t.Fatalf("ID = %q, want %q", resumed.ID, before.ID)
	}
	if resumed.Workspace != before.Workspace {
		t.Fatalf("Workspace = %q, want %q", resumed.Workspace, before.Workspace)
	}
	if resumed.Status != RunStatusRunning {
		t.Fatalf("Status = %q, want %q", resumed.Status, RunStatusRunning)
	}
	waitForRun(t, store, resumed.ID)

	data, err := os.ReadFile(filepath.Join(before.Workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Run
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ID != before.ID || persisted.Workspace != before.Workspace {
		t.Fatalf("persisted = %+v, want same ID/workspace as %+v", persisted, before)
	}
}

func TestStopMarksRunStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "stop runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stop(started[0].ID); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	stopped := findRun(t, store, started[0].ID)
	if stopped.Status != RunStatusStopped {
		t.Fatalf("Status = %q, want %q", stopped.Status, RunStatusStopped)
	}
}

func TestApplyConfigReplacements(t *testing.T) {
	tmp, err := os.CreateTemp("", "test-config-replace-*.py")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())

	content := `LICENSE = "{{license_key}}"`
	if err := os.WriteFile(tmp.Name(), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	mappings := []ConfigMapping{
		{Value: "{{license_key}}"},
	}
	variables := map[string]string{
		"license_key": "my-license-123",
	}
	device := node.DeviceInfo{Serial: "device-123"}

	err = applyConfigReplacements(tmp.Name(), mappings, variables, device)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := `LICENSE = "my-license-123"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func findRun(t *testing.T, store *Store, id string) Run {
	t.Helper()
	for _, run := range store.ListRuns() {
		if run.ID == id {
			return run
		}
	}
	t.Fatalf("run %s not found", id)
	return Run{}
}

func TestResolveValue(t *testing.T) {
	device := node.DeviceInfo{
		Serial: "RZCYA1HFRDA",
		NodeID: "node-123",
	}

	tests := []struct {
		name      string
		value     string
		variables map[string]string
		want      string
	}{
		{
			name:  "Exact built-in serial",
			value: "{{phone.serial}}",
			want:  "RZCYA1HFRDA",
		},
		{
			name:  "Exact built-in node ID",
			value: "{{phone.node_id}}",
			want:  "node-123",
		},
		{
			name:  "Unsupported built-in device serial stays unresolved",
			value: "{{device.serial}}",
			want:  "{{device.serial}}",
		},
		{
			name:  "Spaces and uppercase stays unresolved",
			value: "{{  Phone.Serial  }}",
			want:  "{{  Phone.Serial  }}",
		},
		{
			name:  "Inline built-in",
			value: "my-device-{{phone.serial}}",
			want:  "my-device-RZCYA1HFRDA",
		},
		{
			name:      "Custom variable",
			value:     "{{license}}",
			variables: map[string]string{"license": "LIC-ABC"},
			want:      "LIC-ABC",
		},
		{
			name:      "Nested variables",
			value:     "{{device_id}}",
			variables: map[string]string{"device_id": "{{phone.serial}}"},
			want:      "RZCYA1HFRDA",
		},
		{
			name:      "Unresolved variable stays",
			value:     "{{unknown}}",
			variables: map[string]string{},
			want:      "{{unknown}}",
		},
		{
			name:      "Mixed resolved and unresolved",
			value:     "{{phone.serial}}-{{unknown}}",
			variables: map[string]string{},
			want:      "RZCYA1HFRDA-{{unknown}}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveValue(tt.value, tt.variables, device)
			if got != tt.want {
				t.Errorf("resolveValue(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
