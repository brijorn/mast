//go:build windows

package cli

import (
	"fmt"
	"os"
)

const (
	serviceDir  = "AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup"
	serviceName = "mast.xml"
)

func serviceFileContent(execPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>
  <Actions>
    <Exec>
      <Command>%s</Command>
      <Arguments>start</Arguments>
    </Exec>
  </Actions>
</Task>`, execPath)
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
