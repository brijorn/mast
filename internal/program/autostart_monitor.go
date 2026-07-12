package program

import (
	"log"
	"path/filepath"
	"time"

	"github.com/brijorn/mast/internal/node"
)

const autostartReconnectPollInterval = 5 * time.Second

func (s *Store) monitorAutostartReconnects() {
	s.checkAutostartReconnects()

	ticker := time.NewTicker(autostartReconnectPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.monitorCtx.Done():
			return
		case <-ticker.C:
			s.checkAutostartReconnects()
		}
	}
}

func (s *Store) checkAutostartReconnects() {
	devices, err := s.devices.ListDevices()
	if err != nil {
		log.Printf("autostart reconnect device check failed: %v", err)
		return
	}

	readyBySerial := readySerials(devices)
	var reconnected []string

	s.mu.Lock()
	relevant := make(map[string]struct{}, len(readyBySerial)+len(s.runs))
	for serial := range readyBySerial {
		relevant[serial] = struct{}{}
	}
	for _, state := range s.runs {
		if state.run.Autostart {
			relevant[state.run.Serial] = struct{}{}
		}
	}
	for serial := range relevant {
		ready := readyBySerial[serial]
		previous, observed := s.observedDeviceReady[serial]
		s.observedDeviceReady[serial] = ready
		if observed && !previous && ready {
			reconnected = append(reconnected, serial)
		}
	}
	s.mu.Unlock()

	for _, serial := range reconnected {
		if id := s.autostartRunIDForReconnect(serial); id != "" {
			s.resumeAutostartRunIDs([]string{id}, "autostart reconnect resume failed")
		}
	}
}

func readySerials(devices []node.DeviceInfo) map[string]bool {
	ready := make(map[string]bool, len(devices))
	for _, device := range devices {
		if device.Serial == "" {
			continue
		}
		ready[device.Serial] = ready[device.Serial] || device.State == "device"
	}
	return ready
}

func (s *Store) autostartRunIDsForStartup() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]string, 0)
	for id, state := range s.runs {
		run := state.run
		if autostartRunCanResume(run) && (run.Status == RunStatusStopped || run.Status == RunStatusLost) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Store) autostartRunIDForReconnect(serial string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var selected *Run
	for _, state := range s.runs {
		run := state.run
		if run.Serial != serial {
			continue
		}
		if run.Status == RunStatusRunning || run.Status == RunStatusStarting {
			return ""
		}
		if !autostartRunEligibleForReconnect(run) {
			continue
		}
		if selected == nil || run.StartedAt.After(selected.StartedAt) {
			selected = run
		}
	}
	if selected == nil {
		return ""
	}
	return selected.ID
}

func autostartRunCanResume(run *Run) bool {
	return run.Autostart && !run.AutostartPaused && !run.WorkspaceCleaned && run.Cmd != ""
}

func autostartRunEligibleForReconnect(run *Run) bool {
	if !autostartRunCanResume(run) {
		return false
	}
	switch run.Status {
	case RunStatusStopped, RunStatusLost, RunStatusFailed, RunStatusExited:
		return true
	default:
		return false
	}
}

func (s *Store) resumeAutostartRunIDs(ids []string, errorPrefix string) {
	for _, id := range ids {
		if _, err := s.Resume(ResumeOptions{ID: id}); err != nil {
			s.mu.Lock()
			state := s.runs[id]
			if state != nil {
				state.run.Error = errorPrefix + ": " + err.Error()
				snapshot := nextRunSnapshot(state.run)
				s.mu.Unlock()
				writeRunJSONBestEffort(filepath.Join(snapshot.Workspace, "run.json"), &snapshot)
				continue
			}
			s.mu.Unlock()
		}
	}
}
