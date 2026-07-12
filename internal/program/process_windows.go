//go:build windows

package program

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const windowsCreateNewProcessGroup = 0x00000200

func configureRunCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windowsCreateNewProcessGroup}
}

func configureCompanionRunCommand(cmd *exec.Cmd, _ int) {
	configureRunCommand(cmd)
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
	var errs []error
	for _, companion := range run.Companions {
		if companion.PID <= 0 {
			continue
		}
		if err := taskkillProcessTree(companion.PID); err != nil {
			errs = append(errs, err)
		}
	}
	if run.PID > 0 {
		if err := taskkillProcessTree(run.PID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func taskkillProcessTree(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}
