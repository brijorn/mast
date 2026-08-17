package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWineRunCleansAbandonedOnefileRuntimeBeforeLaunch(t *testing.T) {
	home := t.TempDir()
	phone := "phone-1"
	prefix := filepath.Join(home, ".wine-android-phones", phone)
	temp := filepath.Join(prefix, "drive_c", "users", "runner", "AppData", "Local", "Temp")
	stale := filepath.Join(temp, "onefile_123_456")
	kept := filepath.Join(temp, "operator-data")
	for _, path := range []string{stale, kept} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("prefix"), 0600); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(home, "wine-result")
	fakeWine := "#!/usr/bin/env bash\nprintf '%s\\n' \"$WINEPREFIX\" \"$@\" > \"$WINERUN_TEST_RESULT\"\n"
	if err := os.WriteFile(filepath.Join(bin, "wine"), []byte(fakeWine), 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(home, "game.exe")
	if err := os.WriteFile(executable, []byte("game"), 0600); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "scripts", "winerun")
	command := exec.Command("bash", script, executable, "--level", "7")
	command.Env = append(os.Environ(),
		"HOME="+home,
		"USER=runner",
		"MAST_DEVICE_ID="+phone,
		"PATH="+bin+":"+os.Getenv("PATH"),
		"WINERUN_TEST_RESULT="+result,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("winerun: %v\n%s", err, output)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale onefile runtime still exists: %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("unrelated temp data was removed: %v", err)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{prefix, "game.exe", "--level", "7", ""}, "\n")
	if string(data) != want {
		t.Fatalf("wine invocation = %q, want %q", data, want)
	}
}

func TestWineRunRefusesSharedPrefixWhenPhoneSeedFails(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	called := filepath.Join(home, "wine-called")
	fakeWine := "#!/usr/bin/env bash\ntouch \"$WINERUN_TEST_CALLED\"\n"
	if err := os.WriteFile(filepath.Join(bin, "wine"), []byte(fakeWine), 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(home, "game.exe")
	if err := os.WriteFile(executable, []byte("game"), 0600); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "scripts", "winerun")
	command := exec.Command("bash", script, executable)
	command.Env = append(os.Environ(),
		"HOME="+home,
		"USER=runner",
		"MAST_DEVICE_ID=phone-1",
		"PATH="+bin+":"+os.Getenv("PATH"),
		"WINERUN_TEST_CALLED="+called,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("winerun unexpectedly used the shared prefix: %s", output)
	}
	if !strings.Contains(string(output), "refusing shared-prefix fallback") {
		t.Fatalf("failure did not explain ownership boundary: %s", output)
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatalf("wine was launched after seed failure: %v", err)
	}
}
