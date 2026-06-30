package program

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/node"
)

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
printf 'PYTHONUNBUFFERED=%s\n' "$PYTHONUNBUFFERED"
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
		"PYTHONUNBUFFERED=1",
		"ARGS=--license abc-123",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestStartDoesNotCleanupPreviousWorkspaceForSerial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\necho done\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	firstProgram, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "first runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondProgram, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "second runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	firstStarted, err := store.Start(StartOptions{ProgramID: firstProgram.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, firstStarted[0].ID)
	firstRun := findRun(t, store, firstStarted[0].ID)

	secondStarted, err := store.Start(StartOptions{ProgramID: secondProgram.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, secondStarted[0].ID)

	if _, err := os.Stat(firstRun.Workspace); err != nil {
		t.Fatalf("previous workspace was cleaned on new start: %v", err)
	}
	after := findRun(t, store, firstRun.ID)
	if after.WorkspaceCleaned {
		t.Fatal("previous run WorkspaceCleaned = true, want false")
	}
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

	resumed, err := store.Resume(ResumeOptions{ID: started[0].ID})
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

func TestResumeReplacesLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\necho second\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "resume logs",
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
	run := findRun(t, store, started[0].ID)
	if err := os.WriteFile(filepath.Join(run.Workspace, "stdout.log"), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}

	resumed, err := store.Resume(ResumeOptions{ID: started[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, resumed.ID)
	stdout, _, err := store.Logs(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "second\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "second\n")
	}
}

func TestResumeCanOverrideStartingConfigValues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "config.py"), []byte("MAX_LEVELS = {{MAX_LEVELS}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat config.py\nprintf 'ENV_MAX_LEVELS=%s\\n' \"$MAX_LEVELS\"\n"
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:       "resume config",
		ConfigFile: "config.py",
		Entry:      Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
		ConfigMappings: []ConfigMapping{
			{Key: "MAX_LEVELS", Value: "1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	stdout, _, err := store.Logs(started[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "MAX_LEVELS = 1") || !strings.Contains(stdout, "ENV_MAX_LEVELS=1") {
		t.Fatalf("initial stdout = %q, want starting max level 1", stdout)
	}

	resumed, err := store.Resume(ResumeOptions{
		ID: started[0].ID,
		Variables: map[string]string{
			"MAX_LEVELS": "30",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, resumed.ID)
	stdout, _, err = store.Logs(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "MAX_LEVELS = 30") || !strings.Contains(stdout, "ENV_MAX_LEVELS=30") {
		t.Fatalf("resumed stdout = %q, want resumed max level 30", stdout)
	}
	after := findRun(t, store, resumed.ID)
	if after.Env["MAX_LEVELS"] != "1" {
		t.Fatalf("stored MAX_LEVELS = %q, want original starting value 1", after.Env["MAX_LEVELS"])
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

func TestRunAutostartPersistsAndStopClears(t *testing.T) {
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
		Name:  "autostart runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.SetRunAutostart(started[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Autostart {
		t.Fatalf("Autostart = false, want true")
	}
	data, err := os.ReadFile(filepath.Join(updated.Workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Run
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Autostart {
		t.Fatalf("persisted Autostart = false, want true")
	}

	if _, err := store.Stop(started[0].ID); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	stopped := findRun(t, store, started[0].ID)
	if stopped.Autostart {
		t.Fatalf("Autostart = true, want false after manual stop")
	}
}
