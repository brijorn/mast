//go:build windows

package cli

import (
	"strings"
	"testing"
	"unicode/utf16"
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

func TestServiceFileContentSupervisesTheNode(t *testing.T) {
	content := serviceFileContent(`C:\Users\user\.mast\bin\mast.exe`)

	// A failed action is retried, which covers a crash.
	if !strings.Contains(content, "<RestartOnFailure>") ||
		!strings.Contains(content, "<Count>999</Count>") {
		t.Fatalf("service content does not restart a failed action:\n%s", content)
	}
	// The logon trigger repeats, which covers an exit Windows reads as
	// success. IgnoreNew makes every repetition a no-op while the node runs.
	if !strings.Contains(content, "<Repetition>") ||
		!strings.Contains(content, "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>") {
		t.Fatalf("service content does not re-check a stopped node:\n%s", content)
	}
	if strings.Count(content, "<Interval>PT1M</Interval>") != 2 {
		t.Fatalf("service content does not use one supervision interval:\n%s", content)
	}
	if !strings.Contains(content, "<StartWhenAvailable>true</StartWhenAvailable>") {
		t.Fatalf("service content does not start a missed run:\n%s", content)
	}
}

func TestServiceFileContentRecordsOutput(t *testing.T) {
	content := serviceFileContent(`C:\Users\user\.mast\bin\mast.exe`)

	if !strings.Contains(content, xmlEscape(`>> "C:\Users\user\.mast\service.log" 2>&1`)) {
		t.Fatalf("service content does not record node output:\n%s", content)
	}
}

func TestServiceFileBytesAreUTF16WithBOM(t *testing.T) {
	content := serviceFileContent(`C:\Users\user\.mast\bin\mast.exe`)
	encoded := serviceFileBytes(content)

	if len(encoded) < 2 || encoded[0] != 0xFF || encoded[1] != 0xFE {
		t.Fatalf("service bytes do not open with a little-endian BOM: %v", encoded[:min(2, len(encoded))])
	}
	if !strings.Contains(content, `encoding="UTF-16"`) {
		t.Fatalf("service content does not declare the encoding it is written in:\n%s", content)
	}

	units := make([]uint16, 0, (len(encoded)-2)/2)
	for i := 2; i+1 < len(encoded); i += 2 {
		units = append(units, uint16(encoded[i])|uint16(encoded[i+1])<<8)
	}
	if decoded := string(utf16.Decode(units)); decoded != content {
		t.Fatalf("service bytes do not decode back to the definition:\n%s", decoded)
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
