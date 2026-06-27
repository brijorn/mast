package program

import (
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
printf 'ADB_PORT=%s\n' "$ANDROID_ADB_SERVER_PORT"
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
			{ID: "peer-a", Local: false, ADBHost: "10.0.0.4", ADBPort: 5038},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	registered, err := store.Register(RegisterOptions{
		Path:     source,
		Name:     "test runner",
		Platform: runtime.GOOS,
		Entry:    Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
		INIValues: []INIValue{
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
		"ADB_PORT=5038",
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
			"windows": "wine",
			".py":     "python3 -u",
		},
	}

	// 1. Match by platform
	cmd, args := s.runnerCommand("windows", "test.exe", []string{"arg1", "arg2"})
	if cmd != "wine" {
		t.Errorf("expected cmd to be 'wine', got %q", cmd)
	}
	expectedArgs := []string{"test.exe", "arg1", "arg2"}
	if len(args) != len(expectedArgs) || args[0] != "test.exe" || args[1] != "arg1" || args[2] != "arg2" {
		t.Errorf("expected args to be %v, got %v", expectedArgs, args)
	}

	// 2. Match by file extension
	cmd, args = s.runnerCommand("linux", "test.py", []string{"arg1"})
	if cmd != "python3" {
		t.Errorf("expected cmd to be 'python3', got %q", cmd)
	}
	expectedArgs = []string{"-u", "test.py", "arg1"}
	if len(args) != len(expectedArgs) || args[0] != "-u" || args[1] != "test.py" || args[2] != "arg1" {
		t.Errorf("expected args to be %v, got %v", expectedArgs, args)
	}

	// 3. Fallback (windows executable on linux without config for it)
	s.SetRunners(nil)
	if runtime.GOOS == "linux" {
		cmd, args = s.runnerCommand("windows", "test.exe", []string{"arg1"})
		if cmd != "winerun" {
			t.Errorf("expected fallback cmd to be 'winerun', got %q", cmd)
		}
		expectedArgs = []string{"test.exe", "arg1"}
		if len(args) != len(expectedArgs) || args[0] != "test.exe" || args[1] != "arg1" {
			t.Errorf("expected fallback args to be %v, got %v", expectedArgs, args)
		}
	}
}

func TestCheckPlatform(t *testing.T) {
	s := &Store{}

	// Default behavior when no runner configured
	if runtime.GOOS == "linux" {
		err := s.checkPlatform("windows", "test.exe")
		if err != nil && !strings.Contains(err.Error(), "requires winerun") {
			t.Errorf("unexpected error: %v", err)
		}
	}

	// Custom runner configured
	s.SetRunners(map[string]string{
		"windows": "ls", // "ls" is guaranteed to exist on Unix/Linux
	})
	if runtime.GOOS != "windows" {
		err := s.checkPlatform("windows", "test.exe")
		if err != nil {
			t.Errorf("expected checkPlatform to succeed with available runner 'ls', got err: %v", err)
		}
	}
}
