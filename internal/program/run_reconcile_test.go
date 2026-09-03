package program

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/node"
)

// A run Mast is waiting on must never read `lost`, however often it is polled
// while it finishes.
//
// Reconciling decides from the PID alone, and between the main process dying
// and waitRun recording the exit status the PID is already gone while the
// status is still `running`. Companions widen that window to however long they
// take to be killed and reaped, which is why this reproduces with one. `lost`
// is the state a Mast killed outright leaves behind, so a run wrongly marked
// with it gets relaunched by the startup resume after a clean exit.
func TestListRunsNeverReportsLostWhileMastIsWaiting(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()

	if err := os.WriteFile(filepath.Join(source, "main.sh"),
		[]byte("#!/bin/sh\nsleep 0.2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Outliving main is the point: tearing this down is the window.
	if err := os.WriteFile(filepath.Join(source, "helper.sh"),
		[]byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name: "reconcile window",
		Entry: Entry{
			Command:    "main.sh",
			Companions: []CompanionEntry{{ID: "helper", Command: "helper.sh", Required: true}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	id := started[0].ID

	// Poll the way a caller watching the fleet does, hard enough to land inside
	// the window if it is still open.
	var final string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, run := range store.ListRuns() {
			if run.ID != id {
				continue
			}
			if run.Status == RunStatusLost {
				t.Fatalf("run read as %q while Mast was still waiting on it: %q",
					RunStatusLost, run.Error)
			}
			if run.Status != RunStatusRunning && run.Status != RunStatusStarting {
				final = string(run.Status)
			}
		}
		if final != "" {
			break
		}
	}

	if final != string(RunStatusExited) {
		t.Fatalf("final status = %q, want %q", final, RunStatusExited)
	}
}
