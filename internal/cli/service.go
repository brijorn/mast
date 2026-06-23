package cli

import (
	"os"
	"path/filepath"
)

type ServiceCmd struct {
	Install   ServiceInstallCmd   `cmd:"" help:"Install as OS service"`
	Uninstall ServiceUninstallCmd `cmd:"" help:"Uninstall OS service"`
	Stop      ServiceStopCmd      `cmd:"" help:"Stop OS service"`
}

type ServiceInstallCmd struct{}
type ServiceUninstallCmd struct{}
type ServiceStopCmd struct{}

func servicePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, serviceDir, serviceName), nil
}

func (s *ServiceInstallCmd) Run() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}

	path, err := servicePath()
	if err != nil {
		return err
	}

	content := serviceFileContent(executable)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return err
	}

	return serviceLoad(path)
}

func (s *ServiceUninstallCmd) Run() error {
	path, err := servicePath()
	if err != nil {
		return err
	}

	return serviceUninstall(path)
}

func (s *ServiceStopCmd) Run() error {
	path, err := servicePath()
	if err != nil {
		return err
	}

	return serviceStop(path)
}
