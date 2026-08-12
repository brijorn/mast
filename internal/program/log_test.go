package program

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	all, end, _, reset, err := readLogFileSince(path, 0, start, 0)
	if err != nil {
		t.Fatal(err)
	}
	if all != "33333" || end != 20 || !reset {
		t.Fatalf("all = %q end = %d reset = %v, want retained window ending at 20 with reset", all, end, reset)
	}

	tail, end, _, reset, err := readLogFileSince(path, 15, start, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "33333" || end != 20 || reset {
		t.Fatalf("tail = %q end = %d reset = %v, want last segment without reset", tail, end, reset)
	}
}

func TestRotateRunLogsKeepsThreeGenerations(t *testing.T) {
	workspace := t.TempDir()
	for _, stream := range []string{"stdout", "stderr"} {
		for generation, content := range map[int]string{
			0: "current-" + stream,
			1: "one-" + stream,
			2: "two-" + stream,
			3: "three-" + stream,
		} {
			path := filepath.Join(workspace, stream+".log")
			if generation > 0 {
				path = logGenerationPath(path, generation)
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := rotateRunLogs(workspace); err != nil {
		t.Fatal(err)
	}

	for _, stream := range []string{"stdout", "stderr"} {
		current := filepath.Join(workspace, stream+".log")
		if _, err := os.Stat(current); !os.IsNotExist(err) {
			t.Fatalf("%s current log still exists after rotation: %v", stream, err)
		}
		for generation, want := range map[int]string{
			1: "current-" + stream,
			2: "one-" + stream,
			3: "two-" + stream,
		} {
			data, err := os.ReadFile(logGenerationPath(current, generation))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != want {
				t.Fatalf("%s generation %d = %q, want %q", stream, generation, data, want)
			}
		}
		if _, err := os.Stat(logGenerationPath(current, 4)); !os.IsNotExist(err) {
			t.Fatalf("%s unexpectedly retained a fourth generation: %v", stream, err)
		}
	}
}

// A reader that shows the newest few hundred lines used to download the whole
// run to find them, and a long run's log is capped at ten megabytes.
func TestLogsSinceCanReadOnlyTheTail(t *testing.T) {
	workspace := t.TempDir()
	var builder strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&builder, "line-%04d filler filler filler filler\n", i)
	}
	whole := builder.String()
	if err := os.WriteFile(filepath.Join(workspace, "stdout.log"), []byte(whole), 0600); err != nil {
		t.Fatal(err)
	}
	store := &Store{runs: map[string]*runState{
		"run-1": {run: &Run{ID: "run-1", Workspace: workspace}},
	}}

	tail, err := store.LogsSince("run-1", LogOffsets{TailBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.Stdout) >= len(whole) {
		t.Fatalf("tail returned %d bytes of %d", len(tail.Stdout), len(whole))
	}
	if !strings.HasSuffix(tail.Stdout, "line-4999 filler filler filler filler\n") {
		t.Fatalf("tail did not end at the newest line: %q", tail.Stdout[max(0, len(tail.Stdout)-60):])
	}
	// The cut lands mid-line; the fragment is dropped rather than rendered as
	// though it were a whole one.
	if strings.HasPrefix(tail.Stdout, "line-") == false {
		t.Fatalf("tail should start at a line boundary: %q", tail.Stdout[:40])
	}
	if !tail.StdoutReset {
		t.Fatal("a tail read is a fresh read and must report a reset")
	}
	// The cursor is the real end, so polling continues incrementally.
	if tail.StdoutOffset != int64(len(whole)) {
		t.Fatalf("offset = %d, want %d", tail.StdoutOffset, len(whole))
	}

	// Once the caller holds a cursor it is asking for what is new, and the tail
	// hint must not rewind it.
	rest, err := store.LogsSince("run-1", LogOffsets{Stdout: tail.StdoutOffset, TailBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if rest.Stdout != "" {
		t.Fatalf("expected nothing new, got %d bytes", len(rest.Stdout))
	}
}
