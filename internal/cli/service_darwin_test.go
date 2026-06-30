//go:build darwin

package cli

import (
	"strings"
	"testing"
)

func TestServiceFileContentUsesInstalledBinary(t *testing.T) {
	installPath := "/Users/user/.mast/bin/mast"
	content := serviceFileContent(installPath)

	if !strings.Contains(content, "<string>"+installPath+"</string>") {
		t.Fatalf("service content does not reference installed binary:\n%s", content)
	}
}

func TestServiceLoadReloadsLaunchAgent(t *testing.T) {
	calls := captureServiceCommands(t)
	path := "/Users/user/Library/LaunchAgents/com.brijorn.mast.plist"

	if err := serviceLoad(path); err != nil {
		t.Fatalf("serviceLoad returned error: %v", err)
	}

	assertServiceCommands(t, *calls, []serviceCommandCall{
		{name: "launchctl", args: []string{"unload", path}},
		{name: "launchctl", args: []string{"load", path}},
	})
}

func TestServiceRestartReloadsLaunchAgent(t *testing.T) {
	calls := captureServiceCommands(t)
	path := "/Users/user/Library/LaunchAgents/com.brijorn.mast.plist"

	if err := serviceRestart(path); err != nil {
		t.Fatalf("serviceRestart returned error: %v", err)
	}

	assertServiceCommands(t, *calls, []serviceCommandCall{
		{name: "launchctl", args: []string{"unload", path}},
		{name: "launchctl", args: []string{"load", path}},
	})
}
