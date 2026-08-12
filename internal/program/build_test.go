package program

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newBuildStore makes a Store with just the fields BuildFromSource touches.
func newBuildStore(t *testing.T) *Store {
	t.Helper()
	return &Store{root: t.TempDir(), programs: map[string]Program{}}
}

// sourceFile uses Open so the bytes can be read more than once, as a real
// multipart upload can — a single-use reader would drain after the first build.
func sourceFile(path, content string) UploadFile {
	return UploadFile{Path: path, Open: func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	}}
}

func TestBuildFromSourceBuildsCollectsAndCaches(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	store := newBuildStore(t)

	// A counter outside the (cleaned) source tree proves whether the command
	// ran: a cache hit must not run it again.
	counter := filepath.Join(t.TempDir(), "runs")
	recipe := BuildRecipe{
		Sources:   []string{"myrepo"},
		Workdir:   "myrepo",
		Command:   "printf built > bin && printf x >> " + counter,
		Artifacts: []string{"bin"},
		Name:      "Demo",
		Slug:      "demo",
		Entry:     Entry{Command: "./bin"},
	}
	sources := []UploadFile{sourceFile("myrepo/go.mod", "module demo\n")}

	prog, err := store.BuildFromSource(recipe, sources)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if prog.ID == "" || prog.Slug != "demo" {
		t.Fatalf("unexpected program: %+v", prog)
	}
	// The built artifact is in the registered bundle.
	if _, err := os.Stat(filepath.Join(store.bundlePath(prog.ID), "bin")); err != nil {
		t.Fatalf("artifact not in bundle: %v", err)
	}
	if got := runCount(t, counter); got != 1 {
		t.Fatalf("build command ran %d times, want 1", got)
	}

	prog2, err := store.BuildFromSource(recipe, sources)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if prog2.ID != prog.ID {
		t.Fatalf("cache miss: ids %s vs %s", prog2.ID, prog.ID)
	}
	if got := runCount(t, counter); got != 1 {
		t.Fatalf("cache hit still ran the build (%d times); build-if-stale broken", got)
	}
}

func TestBuildFromSourceRebuildsWhenSourceChanges(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	store := newBuildStore(t)
	recipe := BuildRecipe{
		Sources: []string{"myrepo"}, Workdir: "myrepo",
		Command: "printf built > bin", Artifacts: []string{"bin"},
		Name: "Demo", Slug: "demo", Entry: Entry{Command: "./bin"},
	}
	p1, err := store.BuildFromSource(recipe, []UploadFile{sourceFile("myrepo/go.mod", "v1")})
	if err != nil {
		t.Fatalf("build v1: %v", err)
	}
	p2, err := store.BuildFromSource(recipe, []UploadFile{sourceFile("myrepo/go.mod", "v2 changed")})
	if err != nil {
		t.Fatalf("build v2: %v", err)
	}
	// Same artifact bytes → same content id here, but the cache key differs by
	// source, so the build must have actually re-run. Assert the cache holds
	// two distinct keys.
	entries, _ := os.ReadDir(store.buildCacheDir())
	if len(entries) != 2 {
		t.Fatalf("changed source did not force a rebuild: %d cache keys", len(entries))
	}
	_ = p1
	_ = p2
}

func TestBuildFromSourceValidates(t *testing.T) {
	store := newBuildStore(t)
	if _, err := store.BuildFromSource(BuildRecipe{Entry: Entry{Command: "x"}}, []UploadFile{sourceFile("a", "b")}); err == nil {
		t.Fatal("expected error for missing command")
	}
	if _, err := store.BuildFromSource(BuildRecipe{Command: "true", Entry: Entry{Command: "x"}}, nil); err == nil {
		t.Fatal("expected error for missing source")
	}
}

func runCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read counter: %v", err)
	}
	return len(data)
}
