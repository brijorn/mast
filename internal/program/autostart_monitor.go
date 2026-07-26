package program

import (
	"log"
	"path/filepath"
	"time"

	"github.com/brijorn/mast/internal/node"
)

const autostartReconnectPollInterval = 5 * time.Second

// A program that exits while its device stays connected produces no device
// ready-state transition, so the reconnect watch above never sees it and the
// phone sits idle until a human notices. These bounds govern bringing such a run
// back without turning a program that dies immediately into a restart loop.
const (
	autostartRestartBaseDelay = 30 * time.Second
	autostartRestartMaxDelay  = 15 * time.Minute
	// A run that stayed up this long was doing real work, so its next crash
	// starts from a clean backoff rather than inheriting an old streak.
	autostartRestartHealthyRuntime = 10 * time.Minute
	// Consecutive restarts that never reach healthy runtime. Past this the run is
	// left alone: it is failing on something a restart cannot fix.
	autostartRestartMaxAttempts = 8
	// autostartRestartMaxAge bounds how old a crash may be and still count as a
	// live incident. Without it every historical failure in the store looks
	// restartable the moment this watch first runs, which resurrects runs whose
	// phone has long since moved on to different work.
	autostartRestartMaxAge = 30 * time.Minute
)

// autostartRunEligibleForCrashRestart is deliberately narrower than the
// reconnect predicate. Only a run that ended on its own qualifies: `stopped` is
// an explicit operator or scheduler decision and `lost` belongs to the startup
// path, so resuming either here would override an intent Mast was given.
func autostartRunEligibleForCrashRestart(run *Run) bool {
	if !autostartRunCanResume(run) {
		return false
	}
	return run.Status == RunStatusFailed || run.Status == RunStatusExited
}

type autostartRestartState struct {
	attempts    int
	nextAttempt time.Time
	lastCrashAt time.Time
	exhausted   bool
}

func autostartRestartDelay(attempts int) time.Duration {
	delay := autostartRestartBaseDelay
	for i := 1; i < attempts; i++ {
		delay *= 2
		if delay >= autostartRestartMaxDelay {
			return autostartRestartMaxDelay
		}
	}
	return delay
}

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
			s.checkAutostartRestarts()
		}
	}
}

// checkAutostartRestarts resumes autostart runs that ended on their own while
// the device stayed connected. Eligibility matches the reconnect path; only the
// trigger differs, so a paused, cleaned, or non-autostart run is still left
// alone.
func (s *Store) checkAutostartRestarts() {
	devices, err := s.devices.ListDevices()
	if err != nil {
		log.Printf("autostart restart device check failed: %v", err)
		return
	}
	readyBySerial := readySerials(devices)
	now := time.Now()

	var resume []string
	s.mu.Lock()
	// A phone runs at most one program, so a serial that already has live work
	// is never a restart candidate. Without this the watch happily starts a
	// second run beside a healthy one.
	occupied := make(map[string]bool, len(s.runs))
	for _, state := range s.runs {
		if state.run.Status == RunStatusRunning || state.run.Status == RunStatusStarting {
			occupied[state.run.Serial] = true
		}
	}
	for id, state := range s.runs {
		run := state.run
		if !autostartRunEligibleForCrashRestart(run) {
			// Healthy or ineligible runs must not keep a stale backoff streak.
			if run.Status == RunStatusRunning || run.Status == RunStatusStarting {
				delete(s.autostartRestarts, id)
			}
			continue
		}
		if !readyBySerial[run.Serial] || occupied[run.Serial] {
			continue
		}
		crashedAt := run.StartedAt
		if run.CompletedAt != nil {
			crashedAt = *run.CompletedAt
		}
		if now.Sub(crashedAt) > autostartRestartMaxAge {
			// Old history, not a live incident.
			continue
		}
		restart := s.autostartRestarts[id]
		if restart == nil {
			restart = &autostartRestartState{}
			s.autostartRestarts[id] = restart
		}
		if !restart.lastCrashAt.Equal(crashedAt) {
			// A crash we have not accounted for yet.
			if crashedAt.Sub(run.StartedAt) >= autostartRestartHealthyRuntime {
				restart.attempts = 0
				restart.exhausted = false
			}
			restart.lastCrashAt = crashedAt
			restart.nextAttempt = now.Add(autostartRestartDelay(restart.attempts + 1))
		}
		if restart.exhausted || now.Before(restart.nextAttempt) {
			continue
		}
		if restart.attempts >= autostartRestartMaxAttempts {
			restart.exhausted = true
			log.Printf("autostart restart giving up on run %s after %d consecutive attempts; last error: %s",
				id, restart.attempts, run.Error)
			continue
		}
		restart.attempts++
		restart.nextAttempt = now.Add(autostartRestartDelay(restart.attempts))
		occupied[run.Serial] = true
		log.Printf("autostart restarting run %s on %s (attempt %d/%d, status %s)",
			id, run.Serial, restart.attempts, autostartRestartMaxAttempts, run.Status)
		resume = append(resume, id)
	}
	s.mu.Unlock()

	s.resumeAutostartRunIDs(resume, "autostart restart failed")
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
