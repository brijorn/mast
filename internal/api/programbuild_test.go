package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPackSourceSkipsSiblingProgramsAndBloat proves the packer ships the target
// program and shared code but not sibling programs or dependency caches — the
// monorepo it lives in is ~1 GB of other games, and shipping all of it blew the
// build request's size limit.
func TestPackSourceSkipsSiblingPrograms(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A small dep repo, and a monorepo with the target program, a shared dir, a
	// sibling program, and a dependency cache.
	write("framekit/vision.go")
	write("mono/go.work")
	write("mono/shared/util.go")
	write("mono/programs/target/main.go")
	write("mono/programs/target/assets/a.png")
	write("mono/programs/sibling/huge.bin")
	write("mono/programs/sibling/main.go")
	write("mono/programs/target/node_modules/dep.js")
	write("mono/.venv/lib/pkg.py")

	t.Setenv("MAST_SOURCE_ROOT", root)
	files, err := packSource([]string{"framekit", "mono"}, "mono/programs/target")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.rel] = true
	}
	for _, want := range []string{
		"framekit/vision.go",
		"mono/go.work",
		"mono/shared/util.go",
		"mono/programs/target/main.go",
		"mono/programs/target/assets/a.png",
	} {
		if !got[want] {
			t.Errorf("expected %q to be packed", want)
		}
	}
	for _, unwanted := range []string{
		"mono/programs/sibling/huge.bin",
		"mono/programs/sibling/main.go",
		"mono/programs/target/node_modules/dep.js",
		"mono/.venv/lib/pkg.py",
	} {
		if got[unwanted] {
			t.Errorf("did not expect %q to be packed", unwanted)
		}
	}
}
