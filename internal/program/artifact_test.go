package program

import (
	"os"
	"path/filepath"
	"testing"
)

func artifactStore(t *testing.T) (*Store, string) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "shot.png"), []byte("png-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "debug"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "debug", "frame.png"), []byte("nested"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &Store{runs: map[string]*runState{
		"run-1": {run: &Run{ID: "run-1", Workspace: workspace}},
	}}
	return store, workspace
}

func TestOpenArtifactServesWorkspaceFilesByEitherPathForm(t *testing.T) {
	store, workspace := artifactStore(t)
	for _, name := range []string{"shot.png", filepath.Join(workspace, "shot.png"), "debug/frame.png"} {
		file, info, err := store.OpenArtifact("run-1", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s: empty", name)
		}
		file.Close()
	}
}

// A path arrives from a program's own event payload, so it is untrusted input
// however ordinary it looks. A run is not a file server for the node.
func TestOpenArtifactRefusesAnythingOutsideTheWorkspace(t *testing.T) {
	store, workspace := artifactStore(t)
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("no"), 0600); err != nil {
		t.Fatal(err)
	}
	// A sibling whose name merely starts with the workspace's, which a plain
	// string prefix check would wave through.
	sibling := workspace + "-other"
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "x.png"), []byte("no"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "escape.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks unavailable")
	}

	for _, name := range []string{
		"../" + filepath.Base(outside),
		outside,
		filepath.Join(sibling, "x.png"),
		"escape.png",
		"",
		"debug",
	} {
		if _, _, err := store.OpenArtifact("run-1", name); err == nil {
			t.Fatalf("expected %q to be refused", name)
		}
	}
}

func TestOpenArtifactRejectsAnUnknownRun(t *testing.T) {
	store, _ := artifactStore(t)
	if _, _, err := store.OpenArtifact("nope", "shot.png"); err == nil {
		t.Fatal("expected an unknown run to be refused")
	}
}
