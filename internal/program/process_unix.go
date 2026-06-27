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

	return true, processCwdMatchesRun(run)
}

func killRunProcess(run *Run) error {
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
