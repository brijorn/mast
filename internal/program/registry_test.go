package program

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteProgramRemovesRegistryEntryAndBundleDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}

	registered, err := store.RegisterUpload(RegisterUploadOptions{
		Name:  "delete app",
		Entry: Entry{Command: "run.sh"},
		Files: []UploadFile{
			{Path: "run.sh", Content: strings.NewReader("echo delete\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := store.bundlePath(registered.ID)
	if _, err := os.Stat(bundlePath); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteProgram(registered.ID); err != nil {
		t.Fatal(err)
	}
	if programs := store.ListPrograms(); len(programs) != 0 {
		t.Fatalf("programs = %+v, want empty", programs)
	}
	if _, err := os.Stat(bundlePath); !os.IsNotExist(err) {
		t.Fatalf("bundle stat error = %v, want not exist", err)
	}

	reloaded, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}
	if programs := reloaded.ListPrograms(); len(programs) != 0 {
		t.Fatalf("reloaded programs = %+v, want empty", programs)
	}
}

func TestRegisterUploadDeletesReplacedBundleDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.RegisterUpload(RegisterUploadOptions{
		Name:  "test app",
		Entry: Entry{Command: "run.sh"},
		Files: []UploadFile{
			{Path: "run.sh", Content: strings.NewReader("echo first\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 {
		t.Fatalf("first Version = %d, want 1", first.Version)
	}
	firstPath := store.bundlePath(first.ID)
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatal(err)
	}

	second, err := store.RegisterUpload(RegisterUploadOptions{
		Name:  "test app",
		Entry: Entry{Command: "run.sh"},
		Files: []UploadFile{
			{Path: "run.sh", Content: strings.NewReader("echo second\n")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 {
		t.Fatalf("second Version = %d, want 2", second.Version)
	}
	if first.ID == second.ID {
		t.Fatal("test setup produced identical bundle IDs")
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("old bundle stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(store.bundlePath(second.ID)); err != nil {
		t.Fatal(err)
	}
	programs := store.ListPrograms()
	if len(programs) != 1 || programs[0].ID != second.ID {
		t.Fatalf("programs = %+v, want only replacement bundle %s", programs, second.ID)
	}
	if programs[0].Version != 2 {
		t.Fatalf("registry Version = %d, want 2", programs[0].Version)
	}
}
