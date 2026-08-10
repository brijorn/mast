package program

import "time"

// setRunPID records a started process, together with the start time that tells
// it apart from whatever else may hold that PID later.
func setRunPID(run *Run, pid int) {
	run.PID = pid
	run.PIDStartTime, _ = processStartTime(pid)
}

// clearRunPID forgets a process that has ended. The start time goes with the
// PID: kept alone it would be a claim on a number this run no longer holds.
func clearRunPID(run *Run) {
	run.PID = 0
	run.PIDStartTime = 0
}

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
