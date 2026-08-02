package program

import (
	"fmt"
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
	// A restart incident ends only after this much time without another
	// failure. Runtime alone is not evidence of recovery: a run that repeatedly
	// dies just beyond a fixed healthy threshold must still escalate.
	autostartRestartRecoveryWindow = 6 * time.Hour
	// Restarts spent in one failure incident. Past this the run is left alone:
	// it is failing at a rate that restarts are not correcting.
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
//
// A program that finishes on a clean exit narrows it further: a zero exit from
// one of those is the run reporting that it did the work it was configured for,
// and restarting it makes that configuration unenforceable. A run bounded at
// twenty levels was resumed every time it reached twenty, and because the run
// keeps its progress across a resume it played one more level per attempt and
// reported twenty-eight of a limit of twenty. Programs that end for their own
// reasons — a licensed executable closing after a session — do not declare
// this, and are still resumed whenever they end on their own.
func autostartRunEligibleForCrashRestart(run *Run, finishesOnCleanExit bool) bool {
	if !run.AutostartCrashRestart || !autostartRunCanResume(run) {
		return false
	}
	if run.Status == RunStatusFailed {
		return true
	}
	if run.Status != RunStatusExited {
		return false
	}
	return !(finishesOnCleanExit && run.ExitCode != nil && *run.ExitCode == 0)
}

type autostartRestartState struct {
	nextAttempt time.Time
	lastCrashAt time.Time
}

func scheduleAutostartRestart(supervisor *AutostartSupervisorState, restart *autostartRestartState, from time.Time) {
	next := from.UTC().Add(autostartRestartDelay(supervisor.RestartAttempts + 1))
	restart.nextAttempt = next
	supervisor.NextRestartAt = &next
}

func clearScheduledAutostartRestart(supervisor *AutostartSupervisorState) {
	if supervisor != nil {
		supervisor.NextRestartAt = nil
	}
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

func autostartFailureDescription(run *Run) string {
	if run.Error != "" {
		return run.Error
	}
	if run.ExitCode != nil {
		return fmt.Sprintf("process exited with code %d", *run.ExitCode)
	}
	return "process exited"
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
	now := time.Now().UTC()

	var resume []string
	var changed []Run
	s.mu.Lock()
	// A phone runs at most one program, so a serial that already has live work
	// is never a restart candidate. Without this the watch happily starts a
	// second run beside a healthy one.
	occupied := make(map[string]bool, len(s.runs))
	for _, state := range s.runs {
		if state.resuming || state.run.Status == RunStatusRunning || state.run.Status == RunStatusStarting {
			occupied[state.run.Serial] = true
		}
	}
	for id, state := range s.runs {
		run := state.run
		if state.resuming {
			continue
		}
		if !autostartRunEligibleForCrashRestart(run, s.programs[run.ProgramID].FinishesOnCleanExit) {
			// Recovery is based on time since the last failure, not the current
			// process lifetime. This prevents an 11-minute crash loop from
			// clearing a 10-minute "healthy" threshold on every attempt.
			if (run.Status == RunStatusRunning || run.Status == RunStatusStarting) &&
				run.AutostartSupervisor != nil &&
				run.AutostartSupervisor.LastFailureAt != nil &&
				now.Sub(*run.AutostartSupervisor.LastFailureAt) >= autostartRestartRecoveryWindow {
				run.AutostartSupervisor = nil
				delete(s.autostartRestarts, id)
				changed = append(changed, nextRunSnapshot(run))
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
			if supervisor := run.AutostartSupervisor; supervisor != nil && supervisor.LastFailureAt != nil {
				restart.lastCrashAt = *supervisor.LastFailureAt
				if supervisor.NextRestartAt != nil {
					restart.nextAttempt = supervisor.NextRestartAt.UTC()
				} else {
					scheduleAutostartRestart(supervisor, restart, now)
					changed = append(changed, nextRunSnapshot(run))
				}
			}
			s.autostartRestarts[id] = restart
		}
		if !restart.lastCrashAt.Equal(crashedAt) {
			// A crash we have not accounted for yet.
			supervisor := run.AutostartSupervisor
			if supervisor == nil || supervisor.LastFailureAt == nil ||
				crashedAt.Sub(*supervisor.LastFailureAt) >= autostartRestartRecoveryWindow {
				supervisor = &AutostartSupervisorState{}
				run.AutostartSupervisor = supervisor
			}
			lastFailureAt := crashedAt.UTC()
			supervisor.LastFailureAt = &lastFailureAt
			supervisor.LastError = autostartFailureDescription(run)
			restart.lastCrashAt = crashedAt
			scheduleAutostartRestart(supervisor, restart, now)
			if supervisor.RestartAttempts >= autostartRestartMaxAttempts {
				supervisor.Abandoned = true
				clearScheduledAutostartRestart(supervisor)
				log.Printf("autostart restart giving up on run %s after %d attempts; last error: %s",
					id, supervisor.RestartAttempts, supervisor.LastError)
			}
			changed = append(changed, nextRunSnapshot(run))
		}
		supervisor := run.AutostartSupervisor
		if supervisor == nil || supervisor.Abandoned || now.Before(restart.nextAttempt) {
			continue
		}
		if supervisor.RestartAttempts >= autostartRestartMaxAttempts {
			supervisor.Abandoned = true
			clearScheduledAutostartRestart(supervisor)
			changed = append(changed, nextRunSnapshot(run))
			log.Printf("autostart restart giving up on run %s after %d attempts; last error: %s",
				id, supervisor.RestartAttempts, supervisor.LastError)
			continue
		}
		supervisor.RestartAttempts++
		clearScheduledAutostartRestart(supervisor)
		changed = append(changed, nextRunSnapshot(run))
		occupied[run.Serial] = true
		log.Printf("autostart restarting run %s on %s (attempt %d/%d, status %s)",
			id, run.Serial, supervisor.RestartAttempts, autostartRestartMaxAttempts, run.Status)
		resume = append(resume, id)
	}
	s.mu.Unlock()

	for index := range changed {
		writeRunJSONBestEffort(filepath.Join(changed[index].Workspace, "run.json"), &changed[index])
	}
	s.resumeAutostartRunIDs(resume, "autostart restart failed")
}

func (s *Store) checkAutostartReconnects() {
	devices, err := s.devices.ListDevices()
	if err != nil {
		log.Printf("autostart reconnect device check failed: %v", err)
		return
	}

	readyBySerial := readySerials(devices)
	devicesBySerial := deviceInfosBySerial(devices)
	var reconnected []string
	type readinessObservation struct {
		device node.DeviceInfo
		ready  bool
	}
	var observations []readinessObservation

	s.mu.Lock()
	relevant := make(map[string]struct{}, len(readyBySerial)+len(s.observedDeviceReady)+len(s.runs))
	for serial := range readyBySerial {
		relevant[serial] = struct{}{}
	}
	for serial := range s.observedDeviceReady {
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
		if device, ok := devicesBySerial[serial]; ok {
			s.observedDevices[serial] = device
		}
		device := s.observedDevices[serial]
		if device.Serial == "" {
			device.Serial = serial
		}
		if !observed || previous != ready {
			observations = append(observations, readinessObservation{device: device, ready: ready})
		}
		if observed && !previous && ready {
			reconnected = append(reconnected, serial)
		}
	}
	s.mu.Unlock()

	if observer, ok := s.devices.(interface {
		ObserveDeviceReady(node.DeviceInfo, bool)
	}); ok {
		for _, observation := range observations {
			observer.ObserveDeviceReady(observation.device, observation.ready)
		}
	}

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

func deviceInfosBySerial(devices []node.DeviceInfo) map[string]node.DeviceInfo {
	bySerial := make(map[string]node.DeviceInfo, len(devices))
	for _, device := range devices {
		if device.Serial == "" {
			continue
		}
		existing, ok := bySerial[device.Serial]
		if !ok || (existing.State != "device" && device.State == "device") {
			bySerial[device.Serial] = device
		}
	}
	return bySerial
}

func (s *Store) autostartRunIDsForStartup() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]string, 0)
	for id, state := range s.runs {
		run := state.run
		if run.AutostartReconnect && autostartRunCanResume(run) &&
			(run.Status == RunStatusStopped || run.Status == RunStatusLost) {
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
		if state.resuming || run.Status == RunStatusRunning || run.Status == RunStatusStarting {
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
	return !run.AutostartPaused && !run.WorkspaceCleaned && run.Cmd != ""
}

func autostartRunEligibleForReconnect(run *Run) bool {
	if !run.AutostartReconnect || !autostartRunCanResume(run) {
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
		if _, err := s.Resume(ResumeOptions{ID: id, Supervisor: true}); err != nil {
			s.mu.Lock()
			state := s.runs[id]
			if state != nil {
				message := errorPrefix + ": " + err.Error()
				now := time.Now().UTC()
				state.run.Status = RunStatusFailed
				state.run.CompletedAt = &now
				state.run.Error = message
				if supervisor := state.run.AutostartSupervisor; supervisor != nil {
					supervisor.LastError = message
					supervisor.LastFailureAt = &now
					if supervisor.RestartAttempts >= autostartRestartMaxAttempts {
						supervisor.Abandoned = true
						clearScheduledAutostartRestart(supervisor)
					}
				}
				if restart := s.autostartRestarts[id]; restart != nil {
					restart.lastCrashAt = now
					if supervisor := state.run.AutostartSupervisor; supervisor != nil && !supervisor.Abandoned {
						scheduleAutostartRestart(supervisor, restart, now)
					}
				}
				snapshot := nextRunSnapshot(state.run)
				s.mu.Unlock()
				writeRunJSONBestEffort(filepath.Join(snapshot.Workspace, "run.json"), &snapshot)
				if supervisor := snapshot.AutostartSupervisor; supervisor != nil && supervisor.Abandoned {
					log.Printf("autostart restart giving up on run %s after %d attempts; last error: %s",
						id, supervisor.RestartAttempts, supervisor.LastError)
				}
				continue
			}
			s.mu.Unlock()
		}
	}
}
