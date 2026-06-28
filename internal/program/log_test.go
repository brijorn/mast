package program

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogsSinceReturnsAppendedBytesAndDetectsReset(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instances", "run-1")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "stdout.log"), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}

	store := &Store{
		runs: map[string]*runState{
			"run-1": {run: &Run{ID: "run-1", Workspace: workspace}},
		},
	}

	initial, err := store.LogsSince("run-1", LogOffsets{})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Stdout != "first\n" || initial.StdoutOffset != 6 {
		t.Fatalf("initial logs = %+v, want first newline at offset 6", initial)
	}

	if err := os.WriteFile(filepath.Join(workspace, "stdout.log"), []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := store.LogsSince("run-1", LogOffsets{Stdout: initial.StdoutOffset})
	if err != nil {
		t.Fatal(err)
	}
	if next.Stdout != "second\n" || next.StdoutOffset != 13 || next.StdoutReset {
		t.Fatalf("next logs = %+v, want appended second line at offset 13", next)
	}

	if err := os.WriteFile(filepath.Join(workspace, "stdout.log"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reset, err := store.LogsSince("run-1", LogOffsets{Stdout: next.StdoutOffset})
	if err != nil {
		t.Fatal(err)
	}
	if reset.Stdout != "new\n" || reset.StdoutOffset != 4 || !reset.StdoutReset {
		t.Fatalf("reset logs = %+v, want full new log with reset", reset)
	}
}

func TestBoundedLogWriterCapsSingleFileAndReadsWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	var start int64
	writer, err := newBoundedLogWriter(path, 5, func(next int64) {
		start = next
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("00000111112222233333")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	matches, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("len(log files) = %d, want 1; files = %v", len(matches), matches)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "33333" || start != 15 {
		t.Fatalf("file = %q start = %d, want newest chunk at start 15", data, start)
	}

	all, end, _, reset, err := readLogFileSince(path, 0, start)
	if err != nil {
		t.Fatal(err)
	}
	if all != "33333" || end != 20 || !reset {
		t.Fatalf("all = %q end = %d reset = %v, want retained window ending at 20 with reset", all, end, reset)
	}

	tail, end, _, reset, err := readLogFileSince(path, 15, start)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "33333" || end != 20 || reset {
		t.Fatalf("tail = %q end = %d reset = %v, want last segment without reset", tail, end, reset)
	}
}
