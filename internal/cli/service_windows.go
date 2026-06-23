//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
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
	return exec.Command("schtasks", "/create", "/xml", path, "/tn", "mast").Run()
}

func serviceStop(_ string) error {
	return exec.Command("schtasks", "/end", "/tn", "mast").Run()
}

func serviceUninstall(path string) error {
	if err := exec.Command("schtasks", "/delete", "/tn", "mast", "/f").Run(); err != nil {
		return err
	}
	return os.Remove(path)
}
