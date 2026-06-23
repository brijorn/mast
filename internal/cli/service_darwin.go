//go:build darwin

package cli

import (
	"fmt"
	"os"
	"os/exec"
)

const (
	serviceDir  = "Library/LaunchAgents"
	serviceName = "com.brijorn.mast.plist"
)

func serviceFileContent(execPath string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "...">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.brijorn.mast</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>start</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>`, execPath)
}

func serviceLoad(path string) error {
	if err := exec.Command("launchctl", "load", path).Run(); err != nil {
		return err
	}
	return nil
}

func serviceStop(path string) error {
	if err := exec.Command("launchctl", "unload", path).Run(); err != nil {
		return err
	}
	return nil
}

func serviceUninstall(path string) error {
	if err := serviceStop(path); err != nil {
		return err
	}
	return os.Remove(path)
}
