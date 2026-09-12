package program

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/brijorn/mast/internal/node"
)

func TestStartAndResumeCannotBothOwnPhone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("exec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{devices: []node.DeviceInfo{{Serial: "phone-1", State: "device"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	p, err := registerTestProgram(t, store, source, RegisterUploadOptions{Name: "ownership", Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Start(StartOptions{ProgramID: p.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stop(StopOptions{ID: first[0].ID}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, first[0].ID)
	gate := make(chan struct{})
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-gate
		_, err := store.Start(StartOptions{ProgramID: p.ID, Serials: []string{"phone-1"}})
		errors <- err
	}()
	go func() { defer wg.Done(); <-gate; _, err := store.Resume(ResumeOptions{ID: first[0].ID}); errors <- err }()
	close(gate)
	wg.Wait()
	close(errors)
	succeeded := 0
	for err := range errors {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful launches=%d, want exactly one", succeeded)
	}
	active := 0
	for _, run := range store.ListRuns() {
		if run.Status == RunStatusRunning || run.Status == RunStatusStarting {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active runs=%d", active)
	}
}
