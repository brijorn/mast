package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type serviceCommandCall struct {
	name string
	args []string
}

func captureServiceCommands(t *testing.T) *[]serviceCommandCall {
	t.Helper()

	original := runServiceCommand
	var calls []serviceCommandCall
	runServiceCommand = func(name string, args ...string) error {
		calls = append(calls, serviceCommandCall{
			name: name,
			args: append([]string(nil), args...),
		})
		return nil
	}
	t.Cleanup(func() {
		runServiceCommand = original
	})

	return &calls
}

func assertServiceCommands(t *testing.T, got []serviceCommandCall, want []serviceCommandCall) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("service commands = %#v, want %#v", got, want)
	}
}

func TestServiceInstallPathForOS(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")

	tests := []struct {
		name string
		goos string
		want string
	}{
		{
			name: "linux",
			goos: "linux",
			want: filepath.Join(home, ConfigFileDir, serviceBinDir, "mast"),
		},
		{
			name: "darwin",
			goos: "darwin",
			want: filepath.Join(home, ConfigFileDir, serviceBinDir, "mast"),
		},
		{
			name: "windows",
			goos: "windows",
			want: filepath.Join(home, ConfigFileDir, serviceBinDir, "mast.exe"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serviceInstallPathForOS(home, tt.goos)
			if got != tt.want {
				t.Fatalf("service install path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceEnvironmentPathIncludesMastAndUserBins(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	execPath := serviceInstallPathForOS(home, "linux")
	basePath := strings.Join([]string{
		"/usr/bin",
		filepath.Join(home, ConfigFileDir, serviceBinDir),
	}, ":")

	got := serviceEnvironmentPathForOS(execPath, "linux", basePath)
	entries := strings.Split(got, ":")
	want := []string{
		filepath.Join(home, ConfigFileDir, serviceBinDir),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		"/usr/bin",
	}

	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("service PATH entries = %#v, want %#v", entries, want)
	}
}

func TestServiceEnvironmentPathForWindowsKeepsDynamicPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	execPath := serviceInstallPathForOS(home, "windows")

	got := serviceEnvironmentPathForOS(execPath, "windows", "")
	entries := strings.Split(got, ";")
	if entries[len(entries)-1] != "%PATH%" {
		t.Fatalf("last Windows PATH entry = %q, want %%PATH%%", entries[len(entries)-1])
	}
	if !strings.Contains(got, filepath.Join(home, ConfigFileDir, serviceBinDir)) {
		t.Fatalf("Windows service PATH %q does not include Mast bin dir", got)
	}
}

func TestInstallServiceBinaryCopiesExecutable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source-mast")
	destination := filepath.Join(root, ConfigFileDir, serviceBinDir, "mast")

	if err := os.WriteFile(source, []byte("new binary"), 0750); err != nil {
		t.Fatalf("write source executable: %v", err)
	}
	if err := os.Chmod(source, 0750); err != nil {
		t.Fatalf("chmod source executable: %v", err)
	}

	if err := installServiceBinary(source, destination); err != nil {
		t.Fatalf("installServiceBinary returned error: %v", err)
	}

	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination executable: %v", err)
	}
	if string(got) != "new binary" {
		t.Fatalf("destination executable = %q, want new binary", got)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(destination)
		if err != nil {
			t.Fatalf("stat destination executable: %v", err)
		}
		if got := info.Mode().Perm(); got != 0750 {
			t.Fatalf("destination mode = %v, want 0750", got)
		}
	}
}

func TestInstallServiceBinaryOverwritesExistingExecutable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source-mast")
	destination := filepath.Join(root, ConfigFileDir, serviceBinDir, "mast")

	if err := os.WriteFile(source, []byte("updated binary"), 0755); err != nil {
		t.Fatalf("write source executable: %v", err)
	}
	if err := os.Chmod(source, 0755); err != nil {
		t.Fatalf("chmod source executable: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatalf("create destination dir: %v", err)
	}
	if err := os.WriteFile(destination, []byte("old binary"), 0755); err != nil {
		t.Fatalf("write destination executable: %v", err)
	}

	if err := installServiceBinary(source, destination); err != nil {
		t.Fatalf("installServiceBinary returned error: %v", err)
	}

	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination executable: %v", err)
	}
	if string(got) != "updated binary" {
		t.Fatalf("destination executable = %q, want updated binary", got)
	}
}

func TestInstallServiceBinaryRequiresSource(t *testing.T) {
	err := installServiceBinary("", filepath.Join(t.TempDir(), "mast"))
	if err == nil || !strings.Contains(err.Error(), "source executable required") {
		t.Fatalf("installServiceBinary error = %v, want source executable required", err)
	}
}
