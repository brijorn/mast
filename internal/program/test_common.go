package program

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/node"
)

type fakeDevices struct {
	devices []node.DeviceInfo
	nodes   []node.NodeInfo
}

func (f fakeDevices) ListDevices() ([]node.DeviceInfo, error) {
	return f.devices, nil
}

func (f fakeDevices) DeviceBySerial(serial string) (*node.DeviceInfo, error) {
	device, ok := findDevice(f.devices, serial)
	if !ok {
		return nil, errors.New("device not found: " + serial)
	}
	return &device, nil
}

func (f fakeDevices) ListNodes() []node.NodeInfo {
	return f.nodes
}

type mutableFakeDevices struct {
	mu      sync.Mutex
	devices []node.DeviceInfo
	nodes   []node.NodeInfo
}

func (f *mutableFakeDevices) SetDevices(devices []node.DeviceInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devices = devices
}

func (f *mutableFakeDevices) ListDevices() ([]node.DeviceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]node.DeviceInfo(nil), f.devices...), nil
}

func (f *mutableFakeDevices) DeviceBySerial(serial string) (*node.DeviceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	device, ok := findDevice(f.devices, serial)
	if !ok {
		return nil, errors.New("device not found: " + serial)
	}
	return &device, nil
}

func (f *mutableFakeDevices) ListNodes() []node.NodeInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]node.NodeInfo(nil), f.nodes...)
}

func registerTestProgram(t *testing.T, store *Store, source string, opts RegisterUploadOptions) (*Program, error) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		opts.Files = append(opts.Files, UploadFile{
			Path:    filepath.ToSlash(rel),
			Content: bytes.NewReader(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return store.RegisterUpload(opts)
}

func waitForRun(t *testing.T, store *Store, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, run := range store.ListRuns() {
			if run.ID == id && run.Status != "running" && run.Status != "starting" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", id)
}

func findRun(t *testing.T, store *Store, id string) Run {
	t.Helper()
	for _, run := range store.ListRuns() {
		if run.ID == id {
			return run
		}
	}
	t.Fatalf("run %s not found", id)
	return Run{}
}
