//go:build !windows

package program

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// processStartTime reads a PID's start time from /proc.
//
// The command name sits in parentheses in the middle of the line and may hold
// both spaces and parentheses of its own, so the fields are counted from the
// last ')' rather than from the beginning. Everything after it is positional:
// state is field 3, and start time is field 22.
func processStartTime(pid int) (uint64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data)[end+1:])
	const startTimeField = 22 - 3
	if len(fields) <= startTimeField {
		return 0, false
	}
	started, err := strconv.ParseUint(fields[startTimeField], 10, 64)
	if err != nil {
		return 0, false
	}
	return started, true
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

func procIsZombie(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] == "Z"
}

func processCwdMatchesRun(run *Run) bool {
	if run.PID <= 0 || run.Workspace == "" {
		return false
	}
	cwd, err := os.Readlink("/proc/" + strconv.Itoa(run.PID) + "/cwd")
	if err != nil {
		return false
	}
	return cwd == run.Workspace
}
