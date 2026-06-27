package program

import "time"

func waitForRunProcessExit(run *Run, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, _ := runProcessStatus(run)
		if !alive {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	alive, _ := runProcessStatus(run)
	return !alive
}
