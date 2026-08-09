package program

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brijorn/mast/internal/node"
)

type Store struct {
	root       string
	mu         sync.Mutex
	programs   map[string]Program
	runs       map[string]*runState
	devices    deviceLister
	startCmd   func(command string, args ...string) *exec.Cmd
	runners    map[string]string
	mastAPIURL string

	monitorCtx          context.Context
	monitorCancel       context.CancelFunc
	observedDeviceReady map[string]bool
	observedDevices     map[string]node.DeviceInfo
	autostartRestarts   map[string]*autostartRestartState
	// Read by the exit handler, which runs on the process's own goroutine and
	// so cannot be holding the store's lock when the shutdown that killed it is
	// still holding it.
	shuttingDown atomic.Bool
}

func (s *Store) SetMastAPIURL(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mastAPIURL = value
}

func (s *Store) standardDeviceEnv(device node.DeviceInfo) map[string]string {
	s.mu.Lock()
	apiURL := s.mastAPIURL
	s.mu.Unlock()
	env := map[string]string{
		// DEVICE_SERIAL stays adb-usable because programs pass it to adb -s.
		// DEVICE_ID is the durable identity Runway keys state on; the two
		// differ only for a wireless device.
		"DEVICE_SERIAL":   deviceADBTarget(device),
		"DEVICE_ID":       device.Serial,
		"DEVICE_ADDRESS":  deviceADBTarget(device),
		"DEVICE_PLATFORM": device.Platform,
		"MAST_NODE_ID":    device.NodeID,
	}
	if apiURL != "" {
		env["MAST_API_URL"] = apiURL
	}
	return env
}

type deviceLister interface {
	DeviceBySerial(serial string) (*node.DeviceInfo, error)
	ListDevices() ([]node.DeviceInfo, error)
	ListNodes() []node.NodeInfo
}

type runState struct {
	run              *Run
	cmd              *exec.Cmd
	companionCmds    []*exec.Cmd
	companionWG      sync.WaitGroup
	companionFailure string
	mainExited       bool
	stopping         bool
	resuming         bool
	// checkpointPolledAt is when the program last asked whether a stop is
	// pending. See Run.CheckpointPolledAt: it lives here, outside the persisted
	// run, because it describes this process rather than this run.
	checkpointPolledAt time.Time
}

func NewStore(root string, devices deviceLister) (*Store, error) {
	if root == "" {
		return nil, errors.New("program root required")
	}

	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	s := &Store{
		root:                root,
		programs:            make(map[string]Program),
		runs:                make(map[string]*runState),
		devices:             devices,
		startCmd:            exec.Command,
		monitorCtx:          monitorCtx,
		monitorCancel:       monitorCancel,
		observedDeviceReady: make(map[string]bool),
		observedDevices:     make(map[string]node.DeviceInfo),
		autostartRestarts:   make(map[string]*autostartRestartState),
	}
	if err := os.MkdirAll(s.bundleDir(), 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.instanceDir(), 0700); err != nil {
		return nil, err
	}
	if err := s.loadRegistry(); err != nil {
		return nil, err
	}
	// Restore run history from workspace directories. Runs that were still
	// running or starting when the daemon stopped are marked as lost because
	// Mast no longer owns a process handle for them.
	s.loadRuns()
	go s.resumeAutostartRuns()
	go s.monitorAutostartReconnects()
	return s, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) bundleDir() string {
	return filepath.Join(s.root, "bundles")
}

func (s *Store) instanceDir() string {
	return filepath.Join(s.root, "instances")
}

func (s *Store) bundlePath(id string) string {
	return filepath.Join(s.bundleDir(), id)
}

func (s *Store) registryPath() string {
	return filepath.Join(s.root, RegistryFileName)
}

func (s *Store) loadRegistry() error {
	f, err := os.Open(s.registryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	var registry registryFile
	if err := json.NewDecoder(f).Decode(&registry); err != nil {
		return err
	}
	for _, p := range registry.Programs {
		s.programs[p.ID] = p
	}
	return nil
}

func (s *Store) saveRegistryLocked() error {
	programs := make([]Program, 0, len(s.programs))
	for _, p := range s.programs {
		programs = append(programs, p)
	}
	sort.Slice(programs, func(i, j int) bool {
		return programs[i].CreatedAt.Before(programs[j].CreatedAt)
	})
	return writeJSON(s.registryPath(), registryFile{Programs: programs})
}
