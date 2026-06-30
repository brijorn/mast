//go:build darwin

package cli

import (
	"fmt"
	"os"
)

const (
	serviceDir  = "Library/LaunchAgents"
	serviceName = "com.brijorn.mast.plist"
)

func serviceFileContent(execPath string) string {
	path := xmlEscape(serviceEnvironmentPath(execPath))
	execPath = xmlEscape(execPath)
	return fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "...">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.brijorn.mast</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>%s</string>
    </dict>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>start</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>`, path, execPath)
}

func serviceLoad(path string) error {
	return serviceRestart(path)
}

func serviceStop(path string) error {
	if err := runServiceCommand("launchctl", "unload", path); err != nil {
		return err
	}
	return nil
}

func serviceRestart(path string) error {
	_ = runServiceCommand("launchctl", "unload", path)
	return runServiceCommand("launchctl", "load", path)
}

func serviceUninstall(path string) error {
	if err := serviceStop(path); err != nil {
		return err
	}
	return os.Remove(path)
}
