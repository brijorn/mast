//go:build windows

package cli

import (
	"strings"
	"testing"
)

func TestServiceFileContentUsesInstalledBinary(t *testing.T) {
	installPath := `C:\Users\user\.mast\bin\mast.exe`
	content := serviceFileContent(installPath)

	if !strings.Contains(content, "<Command>cmd.exe</Command>") {
		t.Fatalf("service content does not use command wrapper:\n%s", content)
	}
	if !strings.Contains(content, `C:\Users\user\.mast\bin`) ||
		!strings.Contains(content, `%PATH%`) ||
		!strings.Contains(content, xmlEscape(`"`+installPath+`" start`)) {
		t.Fatalf("service content does not reference installed binary:\n%s", content)
	}
}

func TestServiceLoadRecreatesAndRunsScheduledTask(t *testing.T) {
	calls := captureServiceCommands(t)
	path := `C:\Users\user\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\mast.xml`

	if err := serviceLoad(path); err != nil {
		t.Fatalf("serviceLoad returned error: %v", err)
	}

	assertServiceCommands(t, *calls, []serviceCommandCall{
		{name: "schtasks", args: []string{"/create", "/xml", path, "/tn", "mast", "/f"}},
		{name: "schtasks", args: []string{"/run", "/tn", "mast"}},
	})
}

func TestServiceRestartRunsScheduledTask(t *testing.T) {
	calls := captureServiceCommands(t)

	if err := serviceRestart(""); err != nil {
		t.Fatalf("serviceRestart returned error: %v", err)
	}

	assertServiceCommands(t, *calls, []serviceCommandCall{
		{name: "schtasks", args: []string{"/end", "/tn", "mast"}},
		{name: "schtasks", args: []string{"/run", "/tn", "mast"}},
	})
}
