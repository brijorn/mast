package program

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/node"
	streamcfg "github.com/brijorn/mast/internal/stream"
)

type Store struct {
	root     string
	mu       sync.Mutex
	programs map[string]Program
	runs     map[string]*runState
	devices  deviceLister
	startCmd func(command string, args ...string) *exec.Cmd
	runners  map[string]string

	batteryMu              sync.Mutex
	batteryProtection      mastconfig.BatteryProtection
	batteryProtected       map[string]bool
	batteryStreamStopped   map[string]bool
	batteryMonitorInterval time.Duration
	batteryMonitorStop     chan struct{}
	batteryMonitorOnce     sync.Once
}

type deviceLister interface {
	ListDevices() ([]node.DeviceInfo, error)
	ListNodes() []node.NodeInfo
}

type batteryProtectionDeviceController interface {
	PressKey(serial string, keycode uint32, metaState uint32) error
	StopStream(serial string) error
}

type batteryProtectionStreamRestarter interface {
	EnsureStream(serial string, opts streamcfg.Options) (*node.StreamSession, error)
}

type runState struct {
	run        *Run
	cmd        *exec.Cmd
	stopping   bool
	stopReason string
}

func NewStore(root string, devices deviceLister) (*Store, error) {
	if root == "" {
		return nil, errors.New("program root required")
	}

	s := &Store{
		root:                   root,
		programs:               make(map[string]Program),
		runs:                   make(map[string]*runState),
		devices:                devices,
		startCmd:               exec.Command,
		batteryProtection:      mastconfig.DefaultBatteryProtection(),
		batteryProtected:       make(map[string]bool),
		batteryStreamStopped:   make(map[string]bool),
		batteryMonitorInterval: 30 * time.Second,
		batteryMonitorStop:     make(chan struct{}),
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
	go s.monitorBatteryProtection()
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

func (s *Store) SetBatteryProtection(cfg mastconfig.BatteryProtection) {
	s.batteryMu.Lock()
	defer s.batteryMu.Unlock()
	s.batteryProtection = cfg
	if !cfg.Enabled {
		s.batteryProtected = make(map[string]bool)
		s.batteryStreamStopped = make(map[string]bool)
	}
}

func (s *Store) currentBatteryProtection() mastconfig.BatteryProtection {
	s.batteryMu.Lock()
	defer s.batteryMu.Unlock()
	return s.batteryProtection
}

func (s *Store) monitorBatteryProtection() {
	for {
		select {
		case <-s.batteryMonitorStop:
			return
		case <-time.After(s.batteryMonitorInterval):
			s.evaluateBatteryProtection()
		}
	}
}

func (s *Store) evaluateBatteryProtection() {
	cfg := s.currentBatteryProtection()
	if !cfg.Enabled {
		return
	}
	devices, err := s.devices.ListDevices()
	if err != nil {
		return
	}
	deviceBySerial := make(map[string]node.DeviceInfo, len(devices))
	for _, device := range devices {
		deviceBySerial[device.Serial] = device
	}

	s.mu.Lock()
	activeRunIDs := make(map[string][]string)
	protectedRunIDs := make(map[string][]string)
	for id, state := range s.runs {
		switch state.run.Status {
		case RunStatusRunning, RunStatusStarting:
			activeRunIDs[state.run.Serial] = append(activeRunIDs[state.run.Serial], id)
		case RunStatusStopped:
			if state.run.StoppedReason == "battery_protection" {
				protectedRunIDs[state.run.Serial] = append(protectedRunIDs[state.run.Serial], id)
			}
		}
	}
	s.mu.Unlock()

	for serial, device := range deviceBySerial {
		if batteryProtectionRecoveryCondition(device, cfg) {
			s.recoverBatteryProtection(serial, protectedRunIDs[serial], cfg)
		}
	}

	for serial := range activeRunIDs {
		device, ok := deviceBySerial[serial]
		if !ok || !batteryProtectionCondition(device, cfg) {
			continue
		}
		if !s.markBatteryProtectionTriggered(serial) {
			continue
		}
		for _, id := range activeRunIDs[serial] {
			if cfg.StopProgram {
				_, _ = s.stopRun(id, "battery_protection")
			}
		}
		if controller, ok := s.devices.(batteryProtectionDeviceController); ok {
			if cfg.SendHome {
				_ = controller.PressKey(serial, 3, 0)
			}
			if cfg.StopStream {
				_ = controller.StopStream(serial)
				s.markBatteryStreamStopped(serial)
			}
		}
	}
}

func batteryProtectionCondition(device node.DeviceInfo, cfg mastconfig.BatteryProtection) bool {
	if device.BatteryPercent == nil || *device.BatteryPercent > cfg.MinPercent {
		return false
	}
	return device.PowerHealth == "plugged_draining" || device.PowerHealth == "unplugged_draining"
}

func batteryProtectionRecoveryCondition(device node.DeviceInfo, cfg mastconfig.BatteryProtection) bool {
	if device.BatteryPercent == nil || *device.BatteryPercent < cfg.ResumePercent {
		return false
	}
	return device.PowerHealth == "charging" || device.PowerHealth == "holding" || device.PowerHealth == "full"
}

func (s *Store) markBatteryProtectionTriggered(serial string) bool {
	s.batteryMu.Lock()
	defer s.batteryMu.Unlock()
	if s.batteryProtected == nil {
		s.batteryProtected = make(map[string]bool)
	}
	if s.batteryProtected[serial] {
		return false
	}
	s.batteryProtected[serial] = true
	return true
}

func (s *Store) markBatteryStreamStopped(serial string) {
	s.batteryMu.Lock()
	defer s.batteryMu.Unlock()
	if s.batteryStreamStopped == nil {
		s.batteryStreamStopped = make(map[string]bool)
	}
	s.batteryStreamStopped[serial] = true
}

func (s *Store) recoverBatteryProtection(serial string, runIDs []string, cfg mastconfig.BatteryProtection) {
	s.batteryMu.Lock()
	wasProtected := s.batteryProtected[serial]
	streamStopped := s.batteryStreamStopped[serial]
	if wasProtected {
		delete(s.batteryProtected, serial)
		delete(s.batteryStreamStopped, serial)
	}
	s.batteryMu.Unlock()
	if !wasProtected {
		return
	}

	if cfg.StopProgram {
		for _, id := range runIDs {
			_, _ = s.Resume(ResumeOptions{ID: id})
		}
	}
	if cfg.StopStream && streamStopped {
		if restarter, ok := s.devices.(batteryProtectionStreamRestarter); ok {
			_, _ = restarter.EnsureStream(serial, streamcfg.Options{})
		}
	}
}
