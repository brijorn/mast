package program

import "fmt"

// claimSerialLaunchLocked reserves the device before any launch preparation.
// Start, manual resume, reconnect, and crash recovery all enter this boundary.
// The caller holds mu; release is called only after the run is visible or the
// launch has failed. A stopped status alone is insufficient while waitRun is
// still reaping a process or its companions.
func (s *Store) claimSerialLaunchLocked(serial, exceptRunID string) (func(), error) {
	if s.launchingSerials[serial] {
		return nil, fmt.Errorf("phone %s already has a launch in progress", serial)
	}
	for id, state := range s.runs {
		if id == exceptRunID || state.run.Serial != serial {
			continue
		}
		if state.waiting || state.resuming || state.run.Status == RunStatusRunning || state.run.Status == RunStatusStarting {
			return nil, fmt.Errorf("phone %s is occupied by run %s", serial, id)
		}
	}
	if s.launchingSerials == nil {
		s.launchingSerials = make(map[string]bool)
	}
	s.launchingSerials[serial] = true
	return func() {
		s.mu.Lock()
		delete(s.launchingSerials, serial)
		s.mu.Unlock()
	}, nil
}
