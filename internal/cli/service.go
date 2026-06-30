package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type ServiceCmd struct {
	Install   ServiceInstallCmd   `cmd:"" help:"Install as OS service"`
	Uninstall ServiceUninstallCmd `cmd:"" help:"Uninstall OS service"`
	Stop      ServiceStopCmd      `cmd:"" help:"Stop OS service"`
	Restart   ServiceRestartCmd   `cmd:"" help:"Restart OS service"`
}

type ServiceInstallCmd struct{}
type ServiceUninstallCmd struct{}
type ServiceStopCmd struct{}
type ServiceRestartCmd struct{}

const serviceBinDir = "bin"

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
	_ = serviceStop(path)

	installPath, err := serviceInstallPath()
	if err != nil {
		return err
	}
	if err := installServiceBinary(executable, installPath); err != nil {
		return err
	}

	content := serviceFileContent(installPath)

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

func (s *ServiceRestartCmd) Run() error {
	path, err := servicePath()
	if err != nil {
		return err
	}

	return serviceRestart(path)
}

func serviceInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return serviceInstallPathForOS(home, runtime.GOOS), nil
}

func serviceInstallPathForOS(home string, goos string) string {
	return filepath.Join(home, ConfigFileDir, serviceBinDir, serviceBinaryName(goos))
}

func serviceBinaryName(goos string) string {
	if goos == "windows" {
		return "mast.exe"
	}
	return "mast"
}

func serviceEnvironmentPath(execPath string) string {
	return serviceEnvironmentPathForOS(execPath, runtime.GOOS, "")
}

func serviceEnvironmentPathForOS(execPath string, goos string, basePath string) string {
	separator := string(os.PathListSeparator)
	if goos == "windows" {
		separator = ";"
	}

	entries := []string{filepath.Dir(execPath)}
	if home := serviceHomeFromInstallPath(execPath); home != "" {
		entries = append(entries,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
		)
	}
	if basePath != "" {
		entries = append(entries, strings.Split(basePath, separator)...)
	} else {
		entries = append(entries, defaultServicePathEntries(goos)...)
	}
	if goos == "windows" {
		entries = append(entries, "%PATH%")
	}

	return strings.Join(uniquePathEntries(entries, goos), separator)
}

func serviceHomeFromInstallPath(execPath string) string {
	binDir := filepath.Dir(execPath)
	configDir := filepath.Dir(binDir)
	if filepath.Base(configDir) != ConfigFileDir {
		return ""
	}
	return filepath.Dir(configDir)
}

func defaultServicePathEntries(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	case "windows":
		return nil
	default:
		return []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin", "/usr/games", "/usr/local/games", "/snap/bin"}
	}
}

func uniquePathEntries(entries []string, goos string) []string {
	seen := make(map[string]bool, len(entries))
	var unique []string
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key := entry
		if goos == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, entry)
	}
	return unique
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

func installServiceBinary(source string, destination string) error {
	if source == "" {
		return fmt.Errorf("source executable required")
	}
	if destination == "" {
		return fmt.Errorf("destination executable required")
	}
	if sameFile(source, destination) {
		return nil
	}

	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		_ = sourceFile.Close()
	}()

	sourceInfo, err := sourceFile.Stat()
	if err != nil {
		return err
	}
	if sourceInfo.IsDir() {
		return fmt.Errorf("source executable is a directory: %s", source)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, sourceFile); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(sourceInfo.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return err
	}

	success = true
	return nil
}

func sameFile(a string, b string) bool {
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && os.SameFile(aInfo, bInfo)
}
