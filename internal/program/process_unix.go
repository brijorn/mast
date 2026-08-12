//go:build !windows

package program

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureRunCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func configureCompanionRunCommand(cmd *exec.Cmd, processGroupID int) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: processGroupID}
}

func runProcessStatus(run *Run) (alive bool, matches bool) {
	if run.PID <= 0 {
		return false, false
	}
	process, err := os.FindProcess(run.PID)
	if err != nil {
		return false, false
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return false, false
		}
		return true, false
	}
	if procIsZombie(run.PID) {
		return false, false
	}

	return true, processIdentityMatchesRun(run)
}

// processIdentityMatchesRun reports whether the live PID is still the process
// this run started.
//
// A run recorded before start times were kept has nothing better to be asked
// than where it is standing, so the old test remains for those. It is the
// weaker one — a process is free to move — and it is only ever reached by a
// run that has been alive since before this Mast.
func processIdentityMatchesRun(run *Run) bool {
	if run.PIDStartTime > 0 {
		started, ok := processStartTime(run.PID)
		return ok && started == run.PIDStartTime
	}
	return processCwdMatchesRun(run)
}

func killRunProcess(run *Run) error {
	// The process group goes first and the slice second, though the slice is
	// the more complete kill. Callers read a failure here as "there was no
	// process to kill" and reconcile a run that had already finished; a slice
	// kill reaps the group's leader too, so leading with it would answer that
	// question with the corpse it had just made.
	defer killRunSlice(run.ID)
	if run.PID <= 0 {
		return nil
	}
	if err := syscall.Kill(-run.PID, syscall.SIGKILL); err == nil {
		return nil
	}
	process, err := os.FindProcess(run.PID)
	if err != nil {
		return err
	}
	return process.Kill()
}
