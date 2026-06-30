//go:build linux

package cli

import (
	"strings"
	"testing"
)

func TestServiceFileContentUsesInstalledBinary(t *testing.T) {
	installPath := "/home/user/.mast/bin/mast"
	content := serviceFileContent(installPath)

	if !strings.Contains(content, "ExecStart="+installPath+" start") {
		t.Fatalf("service content does not reference installed binary:\n%s", content)
	}
	if !strings.Contains(content, `Environment="PATH=/home/user/.mast/bin:/home/user/.local/bin:/home/user/bin:`) {
		t.Fatalf("service content does not add user bin dirs to PATH:\n%s", content)
	}
}

func TestServiceLoadEnablesAndRestartsSystemdService(t *testing.T) {
	calls := captureServiceCommands(t)

	if err := serviceLoad(""); err != nil {
		t.Fatalf("serviceLoad returned error: %v", err)
	}

	assertServiceCommands(t, *calls, []serviceCommandCall{
		{name: "systemctl", args: []string{"--user", "daemon-reload"}},
		{name: "systemctl", args: []string{"--user", "enable", serviceName}},
		{name: "systemctl", args: []string{"--user", "restart", serviceName}},
	})
}

func TestServiceRestartUsesSystemdRestart(t *testing.T) {
	calls := captureServiceCommands(t)

	if err := serviceRestart(""); err != nil {
		t.Fatalf("serviceRestart returned error: %v", err)
	}

	assertServiceCommands(t, *calls, []serviceCommandCall{
		{name: "systemctl", args: []string{"--user", "restart", serviceName}},
	})
}
