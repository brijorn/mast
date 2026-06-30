package program

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type countingReadCloser struct {
	io.Reader
	close func()
}

func (c countingReadCloser) Close() error {
	c.close()
	return nil
}

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
}

func TestRegisterUploadOpensAndClosesFilesOneAtATime(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}

	var open int
	var maxOpen int
	var closes int
	files := make([]UploadFile, 0, 50)
	for i := 0; i < 50; i++ {
		path := "file-" + strconv.Itoa(i) + ".txt"
		content := "content-" + strconv.Itoa(i)
		files = append(files, UploadFile{
			Path: path,
			Open: func() (io.ReadCloser, error) {
				open++
				if open > maxOpen {
					maxOpen = open
				}
				return countingReadCloser{
					Reader: strings.NewReader(content),
					close: func() {
						open--
						closes++
					},
				}, nil
			},
		})
	}

	if _, err := store.RegisterUpload(RegisterUploadOptions{
		Name:  "many files",
		Entry: Entry{Command: "file-0.txt"},
		Files: files,
	}); err != nil {
		t.Fatal(err)
	}

	if maxOpen != 1 {
		t.Fatalf("max open files = %d, want 1", maxOpen)
	}
	if closes != len(files) {
		t.Fatalf("closed files = %d, want %d", closes, len(files))
	}
}
