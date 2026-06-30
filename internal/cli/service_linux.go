//go:build linux

package cli

import (
	"fmt"
	"os"
)

const (
	serviceDir  = ".config/systemd/user"
	serviceName = "mast.service"
)

func serviceFileContent(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Mast node

[Service]
ExecStart=%s start

[Install]
WantedBy=default.target`, execPath)
}

func serviceLoad(_ string) error {
	if err := runServiceCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}

	if err := runServiceCommand("systemctl", "--user", "enable", serviceName); err != nil {
		return err
	}

	return serviceRestart("")
}

func serviceStop(_ string) error {
	return runServiceCommand("systemctl", "--user", "stop", serviceName)
}

func serviceRestart(_ string) error {
	return runServiceCommand("systemctl", "--user", "restart", serviceName)
}

func serviceUninstall(path string) error {
	if err := runServiceCommand("systemctl", "--user", "disable", "--now", serviceName); err != nil {
		return err
	}
	return os.Remove(path)
}
