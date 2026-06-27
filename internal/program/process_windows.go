//go:build windows

package program

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const windowsCreateNewProcessGroup = 0x00000200

func configureRunCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windowsCreateNewProcessGroup}
}

func runProcessStatus(run *Run) (alive bool, matches bool) {
	if run.PID <= 0 {
		return false, false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(run.PID), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false, false
	}
	text := strings.TrimSpace(string(out))
	if text == "" || strings.Contains(text, "No tasks are running") {
		return false, false
	}
	return true, true
}

func killRunProcess(run *Run) error {
	if run.PID <= 0 {
		return nil
	}
	return exec.Command("taskkill", "/PID", strconv.Itoa(run.PID), "/T", "/F").Run()
}
