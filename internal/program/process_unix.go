//go:build !windows

package program

import (
	"bytes"
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

	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(run.PID) + "/cmdline")
	if err != nil {
		return true, true
	}
	got := splitProcCmdline(cmdline)
	want := append([]string{run.Cmd}, run.CmdArgs...)
	return true, commandLineMatches(got, want)
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

func splitProcCmdline(data []byte) []string {
	data = bytes.TrimRight(data, "\x00")
	if len(data) == 0 {
		return nil
	}
	raw := bytes.Split(data, []byte{0})
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		parts = append(parts, string(part))
	}
	return parts
}

func procIsZombie(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] == "Z"
}
