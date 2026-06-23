//go:build linux

package cli

import (
	"fmt"
	"os"
	"os/exec"
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
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return err
	}

	return exec.Command("systemctl", "--user", "enable", "--now", serviceName).Run()
}

func serviceStop(_ string) error {
	return exec.Command("systemctl", "--user", "stop", serviceName).Run()
}

func serviceUninstall(path string) error {
	if err := exec.Command("systemctl", "--user", "disable", "--now", serviceName).Run(); err != nil {
		return err
	}
	return os.Remove(path)
}
