//go:build windows

package node

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	windowsCreateNewProcessGroup = 0x00000200
	windowsDetachedProcess       = 0x00000008
)

func scheduleProcessRestartForPlatform(delay time.Duration) error {
	if delay <= 0 {
		delay = restartDelay
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := append([]string(nil), os.Args[1:]...)

	time.AfterFunc(delay, func() {
		command := windowsRestartCommand(executable, args)
		cmd := exec.Command("cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CmdLine:       `/C "` + command + `"`,
			CreationFlags: windowsCreateNewProcessGroup | windowsDetachedProcess,
			HideWindow:    true,
		}
		if err := cmd.Start(); err != nil {
			log.Println("restart:", err)
			return
		}
		os.Exit(0)
	})

	return nil
}

func windowsRestartCommand(executable string, args []string) string {
	parts := []string{
		"ping 127.0.0.1 -n 2 >NUL",
		"&",
		"start \"\"",
		"/D " + quoteWindowsCmdArg(filepath.Dir(executable)),
		quoteWindowsCmdArg(executable),
	}
	for _, arg := range args {
		parts = append(parts, quoteWindowsCmdArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteWindowsCmdArg(arg string) string {
	return `"` + strings.ReplaceAll(arg, `"`, `""`) + `"`
}
