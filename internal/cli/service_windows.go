//go:build windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf16"
)

const (
	serviceDir  = "AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup"
	serviceName = "mast.xml"
)

// restartAttempts is Task Scheduler's cap on consecutive restarts after the
// action fails. The node is meant to stay up for months, so the count only
// exists to satisfy the schema.
const restartAttempts = 999

// supervisionInterval is how often Task Scheduler reconsiders the task. It is
// both the delay before a failed action is retried and the period of the logon
// trigger's repetition, which is what covers an exit Windows does not consider
// a failure.
const supervisionInterval = "PT1M"

func serviceFileContent(execPath string) string {
	path := serviceEnvironmentPath(execPath)
	workingDir := filepath.Dir(execPath)
	arguments := fmt.Sprintf(`/d /c "set "PATH=%s" && "%s" start >> "%s" 2>&1"`,
		path, execPath, serviceLogPath(execPath))
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>%s</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </LogonTrigger>
  </Triggers>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <RestartOnFailure>
      <Interval>%s</Interval>
      <Count>%d</Count>
    </RestartOnFailure>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>
  <Actions>
    <Exec>
      <Command>cmd.exe</Command>
      <Arguments>%s</Arguments>
      <WorkingDirectory>%s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>`, supervisionInterval, supervisionInterval, restartAttempts,
		xmlEscape(arguments), xmlEscape(workingDir))
}

// serviceLogPath is where the service's own output lands. The node otherwise
// writes nowhere a later session can read, so an exit leaves no account of
// itself.
func serviceLogPath(execPath string) string {
	if home := serviceHomeFromInstallPath(execPath); home != "" {
		return filepath.Join(home, ConfigFileDir, "service.log")
	}
	return filepath.Join(filepath.Dir(execPath), "service.log")
}

// serviceFileBytes encodes the task definition as UTF-16LE with a byte order
// mark. schtasks refuses a UTF-8 file outright — "unable to switch the
// encoding" — so a plain write registers no task at all.
func serviceFileBytes(content string) []byte {
	encoded := utf16.Encode([]rune(content))
	out := make([]byte, 0, 2+2*len(encoded))
	out = append(out, 0xFF, 0xFE)
	for _, unit := range encoded {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return out
}

func serviceLoad(path string) error {
	if err := runServiceCommand("schtasks", "/create", "/xml", path, "/tn", "mast", "/f"); err != nil {
		return err
	}
	return runServiceCommand("schtasks", "/run", "/tn", "mast")
}

func serviceStop(_ string) error {
	return runServiceCommand("schtasks", "/end", "/tn", "mast")
}

func serviceRestart(_ string) error {
	_ = runServiceCommand("schtasks", "/end", "/tn", "mast")
	return runServiceCommand("schtasks", "/run", "/tn", "mast")
}

func serviceUninstall(path string) error {
	if err := runServiceCommand("schtasks", "/delete", "/tn", "mast", "/f"); err != nil {
		return err
	}
	return os.Remove(path)
}
