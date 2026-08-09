package program

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/node"
)

func TestRunSliceNameDropsDashes(t *testing.T) {
	// systemd would read every dash as a path separator and demand a parent
	// slice for each segment, so a run's UUID has to arrive as one name.
	got := runSliceName("CC110EDC-F30C-4AFD-B971-1D752E2CE671")
	want := "mast-run-cc110edcf30c4afdb9711d752e2ce671.slice"
	if got != want {
		t.Fatalf("runSliceName = %q, want %q", got, want)
	}
	if strings.Count(got, "-") != 2 {
		t.Fatalf("runSliceName = %q, want a single mast-run parent", got)
	}
	if runSliceName("") != "" {
		t.Fatalf("runSliceName(\"\") = %q, want empty", runSliceName(""))
	}
}

func TestScopedCommandLeavesTheCommandAloneWithoutSystemd(t *testing.T) {
	if systemdRunPath() != "" {
		t.Skip("host can make transient units")
	}
	command, args := scopedCommand("run-1", "/bin/sh", []string{"run.sh"})
	if command != "/bin/sh" || len(args) != 1 || args[0] != "run.sh" {
		t.Fatalf("scopedCommand = %q %q, want the command untouched", command, args)
	}
}

// A stop has to end what the run started, not merely what it left in its own
// process group. This is the wine case in miniature: a process that opens its
// own session is invisible to a process-group kill, and only the run's slice
// still knows it belongs to the run.
func TestStopEndsProcessThatLeftTheProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}
	if systemdRunPath() == "" {
		t.Skip("host cannot make transient units")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nsetsid /bin/sh -c 'echo $$ > escaped.pid; exec sleep 300' &\nsleep 300\n"
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
		Name:  "escaping runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}

	escapedPID := waitForEscapedPID(t, filepath.Join(started[0].Workspace, "escaped.pid"))
	if err := syscall.Kill(escapedPID, 0); err != nil {
		t.Fatalf("escaped process %d is not running before the stop: %v", escapedPID, err)
	}

	if _, err := store.Stop(StopOptions{ID: started[0].ID}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(escapedPID, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(escapedPID, syscall.SIGKILL)
	t.Fatalf("escaped process %d survived the stop", escapedPID)
}

func waitForEscapedPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("escaped pid never appeared at %s", path)
	return 0
}
