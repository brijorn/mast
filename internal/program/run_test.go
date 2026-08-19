package program

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/node"
	"github.com/google/go-cmp/cmp"
)

func TestStartCopiesBundleRendersConfigAndSetsRemoteADBEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}

	config := `[Settings]
DEVICE_ID = old-device
RESOLUTION = 1080x2340
CELL_CONFIG = 1

[LICENSE]
LICENSE_KEY = YOUR-LICENSE-KEY
`
	if err := os.WriteFile(filepath.Join(source, "config.ini"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	script := `#!/bin/sh
cat config.ini
printf '\nSERIAL=%s\n' "$ANDROID_SERIAL"
printf 'SOCKET=%s\n' "$ADB_SERVER_SOCKET"
printf 'ADB_HOST=%s\n' "$ANDROID_ADB_SERVER_ADDRESS"
printf 'ADB_HOST_VAR=%s\n' "$ANDROID_ADB_SERVER_HOST"
printf 'ADB_PORT=%s\n' "$ANDROID_ADB_SERVER_PORT"
printf 'DEVICE_SERIAL=%s\n' "$DEVICE_SERIAL"
printf 'DEVICE_PLATFORM=%s\n' "$DEVICE_PLATFORM"
printf 'MAST_NODE_ID=%s\n' "$MAST_NODE_ID"
printf 'MAST_API_URL=%s\n' "$MAST_API_URL"
printf 'MAST_RUN_ID=%s\n' "$MAST_RUN_ID"
printf 'MAST_DEVICE_ID=%s\n' "$MAST_DEVICE_ID"
printf 'PYTHONUNBUFFERED=%s\n' "$PYTHONUNBUFFERED"
printf 'ARGS=%s\n' "$*"
`
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{
			{Serial: "remote-123", Platform: node.PlatformAndroid, State: "device", NodeID: "peer-a"},
		},
		nodes: []node.NodeInfo{
			{ID: "local", Local: true, Addr: "127.0.0.1"},
			{ID: "peer-a", Local: false, Addr: "10.0.0.4:6271", ADBPort: 5038},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.SetMastAPIURL("http://127.0.0.1:6271")

	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:       "test runner",
		ConfigFile: "config.ini",
		Entry:      Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
		ConfigMappings: []ConfigMapping{
			{Section: "Settings", Key: "DEVICE_ID", Value: "{{phone.serial}}"},
			{Section: "Settings", Key: "RESOLUTION", Value: "{{resolution}}"},
			{Section: "LICENSE", Key: "LICENSE_KEY", Value: "{{program.secret.LICENSE_KEY}}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runs, err := store.Start(StartOptions{
		ProgramID: registered.ID,
		Serials:   []string{"remote-123"},
		Variables: map[string]string{
			"resolution": "720x1600",
		},
		SecretVariables: map[string]string{"LICENSE_KEY": "abc-123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}

	waitForRun(t, store, runs[0].ID)

	stdout, stderr, err := store.Logs(runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{
		"DEVICE_ID = remote-123",
		"RESOLUTION = 720x1600",
		"LICENSE_KEY = abc-123",
		"SERIAL=remote-123",
		"SOCKET=tcp:10.0.0.4:5038",
		"ADB_HOST=10.0.0.4",
		"ADB_HOST_VAR=10.0.0.4",
		"ADB_PORT=5038",
		"DEVICE_SERIAL=remote-123",
		"DEVICE_PLATFORM=android",
		"MAST_NODE_ID=peer-a",
		"MAST_API_URL=http://127.0.0.1:6271",
		"MAST_RUN_ID=" + runs[0].ID,
		"MAST_DEVICE_ID=remote-123",
		"PYTHONUNBUFFERED=1",
		"ARGS=",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(runs[0].Env["LICENSE_KEY"], "abc-123") {
		t.Fatalf("secret leaked into run env: %+v", runs[0].Env)
	}
	secretData, err := os.ReadFile(secretVariablesPath(runs[0].Workspace))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(secretData, []byte("abc-123")) {
		t.Fatal("workspace secret variables did not preserve the license for resume")
	}
	secretInfo, err := os.Stat(secretVariablesPath(runs[0].Workspace))
	if err != nil {
		t.Fatal(err)
	}
	if got := secretInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("secret variables mode = %o, want 600", got)
	}

	resumed, err := store.Resume(ResumeOptions{ID: runs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, resumed.ID)
	stdout, stderr, err = store.Logs(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "LICENSE_KEY = abc-123") {
		t.Fatalf("resumed logs = stdout %q stderr %q, want preserved secret", stdout, stderr)
	}
}

func TestStartFeedsConfiguredVariableToProgramStdinAndResumeUsesOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "read-level.sh"), []byte("#!/bin/sh\nIFS= read -r level\nprintf 'LEVEL=%s\\n' \"$level\"\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{devices: []node.DeviceInfo{
		{Serial: "phone-1", Platform: node.PlatformAndroid, State: "device", NodeID: "local"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "stdin runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"read-level.sh"}, StdinVariable: "CURRENT_LEVEL"},
		ConfigMappings: []ConfigMapping{
			{Key: "CURRENT_LEVEL", Value: "1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runs, err := store.Start(StartOptions{
		ProgramID: registered.ID,
		Serials:   []string{"phone-1"},
		Variables: map[string]string{"CURRENT_LEVEL": "47"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, runs[0].ID)
	stdout, stderr, err := store.Logs(runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "LEVEL=47") {
		t.Fatalf("logs = stdout %q stderr %q, want configured stdin", stdout, stderr)
	}

	resumed, err := store.Resume(ResumeOptions{
		ID:        runs[0].ID,
		Variables: map[string]string{"CURRENT_LEVEL": "48"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, resumed.ID)
	stdout, stderr, err = store.Logs(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "LEVEL=48") {
		t.Fatalf("resumed logs = stdout %q stderr %q, want updated stdin", stdout, stderr)
	}
}

func TestTerminalInputCommandQuotesCommandAndArguments(t *testing.T) {
	command, args, err := terminalInputCommand("/tmp/program's runner", []string{"plain", "two words", "can't"})
	if err != nil {
		if runtime.GOOS == "linux" {
			t.Fatal(err)
		}
		return
	}
	if runtime.GOOS != "linux" {
		if command != "/tmp/program's runner" || !cmp.Equal(args, []string{"plain", "two words", "can't"}) {
			t.Fatalf("terminalInputCommand = %q %q", command, args)
		}
		return
	}
	commandIndex := -1
	for index, arg := range args {
		if arg == "--command" {
			commandIndex = index + 1
			break
		}
	}
	if commandIndex <= 0 || commandIndex >= len(args) {
		t.Fatalf("script args missing --command: %q", args)
	}
	want := `'/tmp/program'"'"'s runner' 'plain' 'two words' 'can'"'"'t'`
	if args[commandIndex] != want {
		t.Fatalf("quoted command = %q, want %q", args[commandIndex], want)
	}
}

func TestSoftStopRequestPersistsAndAcknowledges(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "programs", "instances", "run-soft-stop")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run-soft-stop", Workspace: workspace, Status: RunStatusRunning, StartedAt: time.Now().UTC()}
	store.mu.Lock()
	store.runs[run.ID] = &runState{run: run}
	store.mu.Unlock()
	requested, err := store.RequestStop(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requested.StopRequestedAt == nil {
		t.Fatal("StopRequestedAt is nil")
	}
	status, err := store.StopRequest(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.RequestedAt == nil || status.AcknowledgedAt != nil {
		t.Fatalf("status = %+v", status)
	}
	acknowledged, err := store.AcknowledgeStop(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged.StopAcknowledgedAt == nil {
		t.Fatal("StopAcknowledgedAt is nil")
	}
	data, err := os.ReadFile(filepath.Join(workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("stop_acknowledged_at")) {
		t.Fatalf("run.json = %s", data)
	}
}

func TestCheckpointPollIsReportedLiveAndNotPersisted(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "programs", "instances", "run-checkpoint-poll")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "run-checkpoint-poll", Workspace: workspace, Status: RunStatusRunning, StartedAt: time.Now().UTC()}
	store.mu.Lock()
	store.runs[run.ID] = &runState{run: run}
	store.mu.Unlock()

	// A program that has never asked is indistinguishable from one that cannot.
	if polled := findRun(t, store, run.ID).CheckpointPolledAt; polled != nil {
		t.Fatalf("CheckpointPolledAt = %v before the program asked", polled)
	}
	if _, err := store.StopRequest(run.ID); err != nil {
		t.Fatal(err)
	}
	if findRun(t, store, run.ID).CheckpointPolledAt == nil {
		t.Fatal("CheckpointPolledAt is nil after the program asked")
	}
	if _, err := store.RequestStop(run.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("checkpoint_polled_at")) {
		t.Fatalf("run.json persisted a live observation: %s", data)
	}
}

func TestRunEndingAfterAStopRequestIsStoppedNotExited(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	// Stands in for a program that reaches its checkpoint and leaves cleanly.
	script := "#!/bin/sh\nwhile [ ! -f stop ]; do sleep 0.02; done\nexit 0\n"
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", Platform: node.PlatformAndroid, State: "device", NodeID: "node-1"}},
		nodes:   []node.NodeInfo{{ID: "node-1", Local: true, Addr: "127.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	program, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "Checkpoint Exit",
		Slug:  "checkpoint-exit",
		Entry: Entry{Command: "./run.sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.Start(StartOptions{ProgramID: program.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	run := runs[0]
	if _, err := store.RequestStop(run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Workspace, "stop"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, run.ID)

	stopped := findRun(t, store, run.ID)
	if stopped.Status != RunStatusStopped {
		t.Fatalf("status = %q, want %q", stopped.Status, RunStatusStopped)
	}
	if stopped.ExitCode != nil {
		t.Fatalf("exit code = %v, want none: a stop is not a result", *stopped.ExitCode)
	}
	if autostartRunEligibleForCrashRestart(&stopped, false) {
		t.Fatal("a run stopped on request is eligible for crash restart")
	}
}

func TestStartMakesLocalEntryExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bit is not meaningful on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf 'direct entry ran\\n'\n"
	if err := os.WriteFile(filepath.Join(source, "run-direct"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "direct executable",
		Entry: Entry{Command: "run-direct"},
	})
	if err != nil {
		t.Fatal(err)
	}

	runs, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, runs[0].ID)
	run := findRun(t, store, runs[0].ID)
	if run.Status != RunStatusExited {
		t.Fatalf("run status = %s, want %s: %s", run.Status, RunStatusExited, run.Error)
	}
	if got := filepath.Base(run.Cmd); got != "run-direct" {
		t.Fatalf("run command = %q, want workspace run-direct", run.Cmd)
	}
	info, err := os.Stat(run.Cmd)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0100 == 0 {
		t.Fatalf("workspace entry mode = %v, want owner executable", info.Mode())
	}
	stdout, stderr, err := store.Logs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "direct entry ran") {
		t.Fatalf("stdout = %q, want direct entry output", stdout)
	}
}

func TestCompanionConditionAndSharedLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixtures require Unix process groups")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.sh"), []byte("#!/bin/sh\nprintf 'main started\\n'\nsleep 0.2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "helper.sh"), []byte("#!/bin/sh\nprintf 'helper started\\n'\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name: "companion test",
		Entry: Entry{
			Command: "main.sh",
			Companions: []CompanionEntry{{
				ID: "helper", Command: "helper.sh", Required: true,
				EnabledWhen: CompanionCondition{Variable: "HELPER_ENABLED", Equals: "true"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	disabled, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, disabled[0].ID)
	if got := findRun(t, store, disabled[0].ID); len(got.Companions) != 0 {
		t.Fatalf("disabled companions = %+v, want none", got.Companions)
	}

	enabled, err := store.Start(StartOptions{
		ProgramID: registered.ID, Serials: []string{"phone-1"}, Variables: map[string]string{"HELPER_ENABLED": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, enabled[0].ID)
	run := findRun(t, store, enabled[0].ID)
	if run.Status != RunStatusExited || len(run.Companions) != 1 || run.Companions[0].PID != 0 {
		t.Fatalf("run = %+v, want exited run with stopped companion", run)
	}
	stdout, _, err := store.Logs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "main started") || !strings.Contains(stdout, "helper started") {
		t.Fatalf("shared stdout = %q", stdout)
	}
}

func TestRequiredCompanionExitFailsRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixtures require Unix process groups")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "helper.sh"), []byte("#!/bin/sh\nprintf 'helper failed\\n' >&2\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "required companion",
		Entry: Entry{Command: "main.sh", Companions: []CompanionEntry{{ID: "helper", Command: "helper.sh", Required: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, runs[0].ID)
	run := findRun(t, store, runs[0].ID)
	if run.Status != RunStatusFailed || !strings.Contains(run.Error, "required companion helper exited") {
		t.Fatalf("run status/error = %s %q", run.Status, run.Error)
	}
}

func TestOptionalCompanionStartFailureDoesNotFailRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.sh"), []byte("#!/bin/sh\nprintf 'main completed\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name: "optional companion",
		Entry: Entry{
			Command:    "main.sh",
			Companions: []CompanionEntry{{ID: "optional", Command: "missing-optional-helper"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, runs[0].ID)
	run := findRun(t, store, runs[0].ID)
	if run.Status != RunStatusExited {
		t.Fatalf("run status = %q, want exited; error = %q", run.Status, run.Error)
	}
	if len(run.Companions) != 1 || run.Companions[0].Error == "" {
		t.Fatalf("optional companion = %+v, want recorded start error", run.Companions)
	}
}

func TestStartDoesNotCleanupPreviousWorkspaceForSerial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\necho done\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	firstProgram, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "first runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondProgram, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "second runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	firstStarted, err := store.Start(StartOptions{ProgramID: firstProgram.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, firstStarted[0].ID)
	firstRun := findRun(t, store, firstStarted[0].ID)

	secondStarted, err := store.Start(StartOptions{ProgramID: secondProgram.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, secondStarted[0].ID)

	if _, err := os.Stat(firstRun.Workspace); err != nil {
		t.Fatalf("previous workspace was cleaned on new start: %v", err)
	}
	after := findRun(t, store, firstRun.ID)
	if after.WorkspaceCleaned {
		t.Fatal("previous run WorkspaceCleaned = true, want false")
	}
}

func TestCustomRunners(t *testing.T) {
	tests := []struct {
		name     string
		runner   string
		command  string
		args     []string
		wantCmd  string
		wantArgs []string
	}{
		{
			name:     "exe runner on linux",
			runner:   "/path/to/winerun",
			command:  "test.exe",
			args:     []string{"arg1", "arg2"},
			wantCmd:  "/path/to/winerun",
			wantArgs: []string{"test.exe", "arg1", "arg2"},
		},
		{
			name:     "runner path with spaces",
			runner:   `"/opt/Wine Runner/winerun"`,
			command:  "test.exe",
			args:     []string{"arg1"},
			wantCmd:  "/opt/Wine Runner/winerun",
			wantArgs: []string{"test.exe", "arg1"},
		},
		{
			name:     "runner with arguments",
			runner:   "python3 -u",
			command:  "test.py",
			args:     []string{"arg1"},
			wantCmd:  "python3",
			wantArgs: []string{"-u", "test.py", "arg1"},
		},
		{
			name:     "quoted runner argument",
			runner:   `python3 -u --label "Dice Yatzy"`,
			command:  "test.py",
			args:     []string{"arg1"},
			wantCmd:  "python3",
			wantArgs: []string{"-u", "--label", "Dice Yatzy", "test.py", "arg1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Store{
				runners: map[string]string{
					filepath.Ext(tc.command): tc.runner,
				},
			}

			cmd, args, err := s.runnerCommand(tc.command, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if cmd != tc.wantCmd {
				t.Fatalf("cmd = %q, want %q", cmd, tc.wantCmd)
			}
			if diff := cmp.Diff(tc.wantArgs, args); diff != "" {
				t.Fatalf("args mismatch (-want +got):\n%s", diff)
			}
		})
	}

	s := &Store{}
	if runtime.GOOS != "windows" {
		_, _, err := s.runnerCommand("test.exe", []string{"arg1"})
		if err == nil {
			t.Fatal("expected no-runner error")
		}
	}
}

func TestRunnerCommandRejectsMalformedRunner(t *testing.T) {
	s := &Store{
		runners: map[string]string{
			".py": `python3 "unterminated`,
		},
	}

	_, _, err := s.runnerCommand("test.py", nil)
	if err == nil {
		t.Fatal("runnerCommand returned nil error, want parse error")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("error = %q, want unterminated quote", err)
	}
}

func TestLoadRunsMarksActiveRunsLost(t *testing.T) {
	root := t.TempDir()
	programRoot := filepath.Join(root, "programs")
	instance := filepath.Join(programRoot, "instances", "run-1")
	if err := os.MkdirAll(instance, 0700); err != nil {
		t.Fatal(err)
	}
	run := Run{
		ID:        "run-1",
		ProgramID: "program-1",
		Serial:    "phone-1",
		NodeID:    "node-1",
		Workspace: instance,
		Status:    RunStatusRunning,
		Cmd:       "/bin/sh",
		CmdArgs:   []string{"run.sh"},
		PID:       999999,
		StartedAt: time.Now().UTC(),
	}
	if err := writeJSON(filepath.Join(instance, "run.json"), &run); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(programRoot, fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}

	runs := store.ListRuns()
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
	if runs[0].Status != RunStatusLost {
		t.Fatalf("Status = %q, want %q", runs[0].Status, RunStatusLost)
	}
	if runs[0].CompletedAt != nil {
		t.Fatalf("CompletedAt = %v, want nil", runs[0].CompletedAt)
	}
}

func TestListRunsReconcilesDeadActiveProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", "-c", "true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	workspace := filepath.Join(store.instanceDir(), "dead-active-run")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID:        "dead-active-run",
		ProgramID: "program-1",
		Serial:    "phone-1",
		NodeID:    "node-1",
		Workspace: workspace,
		Status:    RunStatusRunning,
		PID:       cmd.Process.Pid,
		StartedAt: time.Now().UTC(),
	}
	store.mu.Lock()
	store.runs[run.ID] = &runState{run: run, cmd: cmd}
	store.mu.Unlock()

	runs := store.ListRuns()
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
	if runs[0].Status != RunStatusLost {
		t.Fatalf("Status = %q, want %q", runs[0].Status, RunStatusLost)
	}
	if runs[0].PID != 0 {
		t.Fatalf("PID = %d, want 0", runs[0].PID)
	}
	if !strings.Contains(runs[0].Error, "process finished") {
		t.Fatalf("Error = %q, want process finished message", runs[0].Error)
	}

	var persisted Run
	data, err := os.ReadFile(filepath.Join(workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != RunStatusLost {
		t.Fatalf("persisted Status = %q, want %q", persisted.Status, RunStatusLost)
	}
}

func TestResumeReusesRunIDAndWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\necho resumed\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "resume runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	before := findRun(t, store, started[0].ID)

	resumed, err := store.Resume(ResumeOptions{ID: started[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != before.ID {
		t.Fatalf("ID = %q, want %q", resumed.ID, before.ID)
	}
	if resumed.Workspace != before.Workspace {
		t.Fatalf("Workspace = %q, want %q", resumed.Workspace, before.Workspace)
	}
	if resumed.Status != RunStatusRunning {
		t.Fatalf("Status = %q, want %q", resumed.Status, RunStatusRunning)
	}
	waitForRun(t, store, resumed.ID)

	data, err := os.ReadFile(filepath.Join(before.Workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Run
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ID != before.ID || persisted.Workspace != before.Workspace {
		t.Fatalf("persisted = %+v, want same ID/workspace as %+v", persisted, before)
	}
}

func TestResumeReplacesLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\necho second\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "resume logs",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	run := findRun(t, store, started[0].ID)
	if err := os.WriteFile(filepath.Join(run.Workspace, "stdout.log"), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}

	resumed, err := store.Resume(ResumeOptions{ID: started[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, resumed.ID)
	stdout, _, err := store.Logs(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "second\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "second\n")
	}
	if _, err := os.Stat(filepath.Join(run.Workspace, "stdout.1.log")); !os.IsNotExist(err) {
		t.Fatalf("clean exit unexpectedly retained log history: %v", err)
	}
}

func TestResumeFailedRunRotatesPreviousAttemptLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
attempt=1
if [ -f .attempt ]; then attempt=2; fi
touch .attempt
echo "stdout-attempt-$attempt"
echo "stderr-attempt-$attempt" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "failed resume logs",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	run := findRun(t, store, started[0].ID)
	if run.Status != RunStatusFailed {
		t.Fatalf("first status = %q, want failed", run.Status)
	}

	resumed, err := store.Resume(ResumeOptions{ID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, resumed.ID)

	for _, stream := range []string{"stdout", "stderr"} {
		current, err := os.ReadFile(filepath.Join(run.Workspace, stream+".log"))
		if err != nil {
			t.Fatal(err)
		}
		previous, err := os.ReadFile(filepath.Join(run.Workspace, stream+".1.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(current), stream+"-attempt-2") {
			t.Fatalf("current %s = %q, want second attempt", stream, current)
		}
		if !strings.Contains(string(previous), stream+"-attempt-1") {
			t.Fatalf("previous %s = %q, want first attempt", stream, previous)
		}
	}
}

func TestResumeCanOverrideStartingConfigValues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "config.py"), []byte("MAX_LEVELS = {{MAX_LEVELS}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat config.py\nprintf 'ENV_MAX_LEVELS=%s\\n' \"$MAX_LEVELS\"\n"
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:       "resume config",
		ConfigFile: "config.py",
		Entry:      Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
		ConfigMappings: []ConfigMapping{
			{Key: "MAX_LEVELS", Value: "1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	stdout, _, err := store.Logs(started[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "MAX_LEVELS = 1") || !strings.Contains(stdout, "ENV_MAX_LEVELS=1") {
		t.Fatalf("initial stdout = %q, want starting max level 1", stdout)
	}

	resumed, err := store.Resume(ResumeOptions{
		ID: started[0].ID,
		Variables: map[string]string{
			"MAX_LEVELS": "30",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, resumed.ID)
	stdout, _, err = store.Logs(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "MAX_LEVELS = 30") || !strings.Contains(stdout, "ENV_MAX_LEVELS=30") {
		t.Fatalf("resumed stdout = %q, want resumed max level 30", stdout)
	}
	// The override belongs to the run from here on, not to the one attempt that
	// carried it. A crash-restart supervisor resumes with no variables of its
	// own, so a value kept only for that call would be reverted by the first
	// crash — leaving a run quietly executing the configuration its operator
	// replaced.
	after := findRun(t, store, resumed.ID)
	if after.Env["MAX_LEVELS"] != "30" {
		t.Fatalf("stored MAX_LEVELS = %q, want the resumed value 30", after.Env["MAX_LEVELS"])
	}

	supervised, err := store.Resume(ResumeOptions{ID: resumed.ID, Supervisor: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, supervised.ID)
	stdout, _, err = store.Logs(supervised.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "MAX_LEVELS = 30") || !strings.Contains(stdout, "ENV_MAX_LEVELS=30") {
		t.Fatalf("crash-restarted stdout = %q, want the resumed max level 30", stdout)
	}
}

func TestStopMarksRunStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "stop runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stop(StopOptions{ID: started[0].ID}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	stopped := findRun(t, store, started[0].ID)
	if stopped.Status != RunStatusStopped {
		t.Fatalf("Status = %q, want %q", stopped.Status, RunStatusStopped)
	}
}

func TestStopReconcilesAlreadyFinishedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", "-c", "true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	workspace := filepath.Join(store.instanceDir(), "finished-run")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID:        "finished-run",
		ProgramID: "program-1",
		Serial:    "phone-1",
		NodeID:    "node-1",
		Workspace: workspace,
		Status:    RunStatusRunning,
		PID:       cmd.Process.Pid,
		StartedAt: time.Now().UTC(),
	}
	store.mu.Lock()
	store.runs[run.ID] = &runState{run: run, cmd: cmd}
	store.mu.Unlock()

	stopped, err := store.Stop(StopOptions{ID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != RunStatusStopped {
		t.Fatalf("Status = %q, want %q", stopped.Status, RunStatusStopped)
	}
	if stopped.PID != 0 {
		t.Fatalf("PID = %d, want 0", stopped.PID)
	}
	if stopped.CompletedAt == nil {
		t.Fatal("CompletedAt is nil, want stop timestamp")
	}

	var persisted Run
	data, err := os.ReadFile(filepath.Join(workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status != RunStatusStopped {
		t.Fatalf("persisted Status = %q, want %q", persisted.Status, RunStatusStopped)
	}
}

func TestRunAutostartPersistsAndStopPreserves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(filepath.Join(root, "programs"), fakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "autostart runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.SetRunAutostart(started[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Autostart {
		t.Fatalf("Autostart = false, want true")
	}
	if !updated.AutostartReconnect || !updated.AutostartCrashRestart {
		t.Fatalf("behavior flags = reconnect %v crash %v, want true/true",
			updated.AutostartReconnect, updated.AutostartCrashRestart)
	}
	data, err := os.ReadFile(filepath.Join(updated.Workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Run
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Autostart {
		t.Fatalf("persisted Autostart = false, want true")
	}
	if !persisted.AutostartReconnect || !persisted.AutostartCrashRestart {
		t.Fatalf("persisted behavior flags = reconnect %v crash %v, want true/true",
			persisted.AutostartReconnect, persisted.AutostartCrashRestart)
	}

	if _, err := store.Stop(StopOptions{ID: started[0].ID}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	stopped := findRun(t, store, started[0].ID)
	if !stopped.Autostart {
		t.Fatalf("Autostart = false, want true after manual stop")
	}
	data, err = os.ReadFile(filepath.Join(stopped.Workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Autostart {
		t.Fatalf("persisted Autostart = false, want true after manual stop")
	}
}

func TestRunAutostartBehaviorsToggleIndependently(t *testing.T) {
	workspace := t.TempDir()
	run := &Run{
		SchemaVersion: runSchemaVersion,
		ID:            "run-1",
		Workspace:     workspace,
		Status:        RunStatusStopped,
		Cmd:           "/bin/sh",
	}
	store := &Store{
		runs:              map[string]*runState{run.ID: {run: run}},
		autostartRestarts: make(map[string]*autostartRestartState),
	}

	updated, err := store.SetRunAutostart(run.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.AutostartReconnect || !updated.AutostartCrashRestart {
		t.Fatalf("legacy enable did not set both behaviors: %+v", updated)
	}

	run.AutostartSupervisor = &AutostartSupervisorState{RestartAttempts: 2}
	store.autostartRestarts[run.ID] = &autostartRestartState{}
	crashDisabled := false
	updated, err = store.UpdateRunAutostart(run.ID, AutostartOptions{
		CrashRestart: &crashDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Autostart || !updated.AutostartReconnect || updated.AutostartCrashRestart {
		t.Fatalf("flags after reconnect-only update = autostart %v reconnect %v crash %v",
			updated.Autostart, updated.AutostartReconnect, updated.AutostartCrashRestart)
	}
	if updated.AutostartSupervisor != nil || store.autostartRestarts[run.ID] != nil {
		t.Fatal("disabling crash restart did not clear its supervisor state")
	}

	crashEnabled := true
	updated, err = store.UpdateRunAutostart(run.ID, AutostartOptions{
		CrashRestart: &crashEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.AutostartReconnect || !updated.AutostartCrashRestart {
		t.Fatalf("enabling crash changed reconnect: reconnect %v crash %v",
			updated.AutostartReconnect, updated.AutostartCrashRestart)
	}

	reconnectDisabled := false
	updated, err = store.UpdateRunAutostart(run.ID, AutostartOptions{
		Reconnect: &reconnectDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Autostart || updated.AutostartReconnect || !updated.AutostartCrashRestart {
		t.Fatalf("flags after crash-only update = autostart %v reconnect %v crash %v",
			updated.Autostart, updated.AutostartReconnect, updated.AutostartCrashRestart)
	}

	var persisted Run
	data, err := os.ReadFile(filepath.Join(workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.AutostartReconnect || !persisted.AutostartCrashRestart {
		t.Fatalf("persisted flags = reconnect %v crash %v, want false/true",
			persisted.AutostartReconnect, persisted.AutostartCrashRestart)
	}
}

func TestLoadRunsMigratesLegacyAutostartToBothBehaviors(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "instances", "run-1")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"schema_version": 1,
		"revision":       4,
		"id":             "run-1",
		"workspace":      workspace,
		"status":         RunStatusStopped,
		"autostart":      true,
		"cmd":            "/bin/sh",
		"started_at":     time.Now().UTC(),
	}
	if err := writeJSON(filepath.Join(workspace, "run.json"), legacy); err != nil {
		t.Fatal(err)
	}

	store := &Store{root: root, runs: make(map[string]*runState)}
	store.loadRuns()

	run := store.runs["run-1"].run
	if !run.Autostart || !run.AutostartReconnect || !run.AutostartCrashRestart {
		t.Fatalf("migrated flags = autostart %v reconnect %v crash %v, want true/true/true",
			run.Autostart, run.AutostartReconnect, run.AutostartCrashRestart)
	}
	if run.SchemaVersion != runSchemaVersion {
		t.Fatalf("schema version = %d, want %d", run.SchemaVersion, runSchemaVersion)
	}

	var persisted Run
	data, err := os.ReadFile(filepath.Join(workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SchemaVersion != runSchemaVersion ||
		!persisted.AutostartReconnect || !persisted.AutostartCrashRestart {
		t.Fatalf("persisted migrated run = %+v", persisted)
	}
}

func TestPausedAutostartDoesNotResumeOnStartupOrReconnect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	devices := &mutableFakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	}
	store, err := NewStore(filepath.Join(root, "programs"), devices)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "paused autostart runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	reconnectEnabled := true
	crashDisabled := false
	if _, err := store.UpdateRunAutostart(started[0].ID, AutostartOptions{
		Reconnect: &reconnectEnabled, CrashRestart: &crashDisabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stop(StopOptions{ID: started[0].ID, AutostartPaused: true}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)

	if ids := store.autostartRunIDsForStartup(); len(ids) != 0 {
		t.Fatalf("startup autostart ids = %+v, want none", ids)
	}

	devices.SetDevices(nil)
	store.checkAutostartReconnects()
	devices.SetDevices([]node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}})
	store.checkAutostartReconnects()

	paused := findRun(t, store, started[0].ID)
	if paused.Status != RunStatusStopped {
		t.Fatalf("Status = %q, want %q", paused.Status, RunStatusStopped)
	}
	if !paused.Autostart || !paused.AutostartPaused {
		t.Fatalf("run autostart flags = autostart %v paused %v, want true/true", paused.Autostart, paused.AutostartPaused)
	}

	resumed, err := store.Resume(ResumeOptions{ID: started[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.AutostartPaused {
		t.Fatalf("AutostartPaused = true after explicit resume, want false")
	}
}

func TestAutostartReconnectDoesNotResumeWhileContinuouslyOnline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	devices := &mutableFakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	}
	store, err := NewStore(filepath.Join(root, "programs"), devices)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "autostart reconnect runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRunAutostart(started[0].ID, true); err != nil {
		t.Fatal(err)
	}

	store.checkAutostartReconnects()
	if _, err := store.Stop(StopOptions{ID: started[0].ID}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)
	store.checkAutostartReconnects()

	stopped := findRun(t, store, started[0].ID)
	if stopped.Status != RunStatusStopped {
		t.Fatalf("Status = %q, want %q", stopped.Status, RunStatusStopped)
	}
	if !stopped.Autostart {
		t.Fatalf("Autostart = false, want true")
	}
}

func TestAutostartReconnectResumesAfterDeviceReturns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	// A device that leaves takes its run down with it, so the run this watch
	// exists for is a failed one. The program comes up healthy on the second
	// launch, which is what makes the resume visible as a running run.
	script := "#!/bin/sh\nif [ ! -f crashed ]; then touch crashed; exit 1; fi\nsleep 10\n"
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	devices := &mutableFakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	}
	store, err := NewStore(filepath.Join(root, "programs"), devices)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "autostart reconnect runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	reconnectEnabled := true
	crashDisabled := false
	if _, err := store.UpdateRunAutostart(started[0].ID, AutostartOptions{
		Reconnect: &reconnectEnabled, CrashRestart: &crashDisabled,
	}); err != nil {
		t.Fatal(err)
	}
	store.checkAutostartReconnects()
	waitForRun(t, store, started[0].ID)
	if failed := findRun(t, store, started[0].ID); failed.Status != RunStatusFailed {
		t.Fatalf("Status = %q, want %q", failed.Status, RunStatusFailed)
	}

	devices.SetDevices(nil)
	store.checkAutostartReconnects()
	devices.SetDevices([]node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}})
	store.checkAutostartReconnects()

	resumed := findRun(t, store, started[0].ID)
	if resumed.Status != RunStatusRunning && resumed.Status != RunStatusStarting {
		t.Fatalf("Status = %q, want running or starting", resumed.Status)
	}
	if resumed.Workspace != started[0].Workspace {
		t.Fatalf("Workspace = %q, want %q", resumed.Workspace, started[0].Workspace)
	}
	if !resumed.Autostart {
		t.Fatalf("Autostart = false, want true")
	}
	if !resumed.AutostartReconnect || resumed.AutostartCrashRestart {
		t.Fatalf("behavior flags = reconnect %v crash %v, want true/false",
			resumed.AutostartReconnect, resumed.AutostartCrashRestart)
	}
}

func TestAutostartReconnectDoesNotResumeStoppedRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	devices := &mutableFakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	}
	store, err := NewStore(filepath.Join(root, "programs"), devices)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "autostart reconnect runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRunAutostart(started[0].ID, true); err != nil {
		t.Fatal(err)
	}
	store.checkAutostartReconnects()

	// Stopped on purpose, with autostart left armed for a later resume. A phone
	// that then drops off and returns must not overturn that: the run was not
	// running when the device went, so the reconnect has nothing to restore.
	if _, err := store.Stop(StopOptions{ID: started[0].ID}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)

	devices.SetDevices(nil)
	store.checkAutostartReconnects()
	devices.SetDevices([]node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}})
	store.checkAutostartReconnects()

	stopped := findRun(t, store, started[0].ID)
	if stopped.Status != RunStatusStopped {
		t.Fatalf("Status = %q, want %q", stopped.Status, RunStatusStopped)
	}
	if !stopped.AutostartReconnect {
		t.Fatalf("AutostartReconnect = false, want the run left armed for an explicit resume")
	}
}

func TestAutostartRestartDelayBacksOffAndCaps(t *testing.T) {
	first := autostartRestartDelay(1)
	if first != autostartRestartBaseDelay {
		t.Fatalf("first delay = %s, want %s", first, autostartRestartBaseDelay)
	}
	if second := autostartRestartDelay(2); second != 2*autostartRestartBaseDelay {
		t.Fatalf("second delay = %s, want %s", second, 2*autostartRestartBaseDelay)
	}
	previous := time.Duration(0)
	for attempts := 1; attempts <= autostartRestartMaxAttempts; attempts++ {
		delay := autostartRestartDelay(attempts)
		if delay < previous {
			t.Fatalf("delay for attempt %d went backwards: %s after %s", attempts, delay, previous)
		}
		if delay > autostartRestartMaxDelay {
			t.Fatalf("delay for attempt %d = %s, exceeds cap %s", attempts, delay, autostartRestartMaxDelay)
		}
		previous = delay
	}
	if autostartRestartDelay(64) != autostartRestartMaxDelay {
		t.Fatalf("far-out attempt did not saturate at %s", autostartRestartMaxDelay)
	}
}

// A program that exits on its own leaves the device connected, so no ready-state
// transition occurs and the reconnect watch never fires. Without a separate
// restart the phone stays idle.
func TestAutostartRestartsRunThatExitedWhileDeviceStayedOnline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}

	devices := &mutableFakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	}
	store, err := NewStore(filepath.Join(root, "programs"), devices)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "autostart crash runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	id := started[0].ID
	reconnectDisabled := false
	crashEnabled := true
	if _, err := store.UpdateRunAutostart(id, AutostartOptions{
		Reconnect: &reconnectDisabled, CrashRestart: &crashEnabled,
	}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, id)

	crashed := findRun(t, store, id)
	if crashed.Status != RunStatusFailed && crashed.Status != RunStatusExited {
		t.Fatalf("Status = %q, want failed or exited", crashed.Status)
	}

	// The device never left, so the reconnect watch must not act on this.
	store.checkAutostartReconnects()
	if findRun(t, store, id).Status == RunStatusRunning {
		t.Fatal("reconnect watch resumed a run whose device never disconnected")
	}

	// The first observed crash schedules a backoff rather than restarting now.
	store.checkAutostartRestarts()
	store.mu.Lock()
	restart := store.autostartRestarts[id]
	store.mu.Unlock()
	if restart == nil {
		t.Fatal("crashed autostart run was not tracked for restart")
	}
	if supervisor := findRun(t, store, id).AutostartSupervisor; supervisor == nil || supervisor.RestartAttempts != 0 {
		t.Fatalf("supervisor = %+v before the backoff elapsed, want zero attempts", supervisor)
	} else if supervisor.NextRestartAt == nil || !supervisor.NextRestartAt.After(time.Now()) {
		t.Fatalf("supervisor next restart = %v, want a visible future backoff deadline", supervisor.NextRestartAt)
	}

	// Once it elapses the run is resumed, and the attempt is counted.
	store.mu.Lock()
	store.autostartRestarts[id].nextAttempt = time.Now().Add(-time.Second)
	store.mu.Unlock()
	store.checkAutostartRestarts()

	supervisor := findRun(t, store, id).AutostartSupervisor
	if supervisor == nil {
		t.Fatal("run lost supervisor state after restart")
	}
	attempts := supervisor.RestartAttempts
	if attempts != 1 {
		t.Fatalf("attempts = %d after the backoff elapsed, want 1", attempts)
	}
	if supervisor.NextRestartAt != nil {
		t.Fatalf("running restart retained stale pending deadline %v", supervisor.NextRestartAt)
	}
	restarted := findRun(t, store, id)
	if restarted.AutostartReconnect || !restarted.AutostartCrashRestart {
		t.Fatalf("behavior flags = reconnect %v crash %v, want false/true",
			restarted.AutostartReconnect, restarted.AutostartCrashRestart)
	}
}

// A program that dies instantly must not be restarted forever.
func TestAutostartRestartStopsAfterMaxAttempts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}

	devices := &mutableFakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	}
	store, err := NewStore(filepath.Join(root, "programs"), devices)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "autostart crash loop runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	id := started[0].ID
	if _, err := store.SetRunAutostart(id, true); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, id)

	// Collapse every backoff so the cap, not the clock, is what stops this.
	for i := 0; i < autostartRestartMaxAttempts*3; i++ {
		store.mu.Lock()
		if state := store.autostartRestarts[id]; state != nil {
			state.nextAttempt = time.Now().Add(-time.Second)
		}
		store.mu.Unlock()
		store.checkAutostartRestarts()
		waitForRun(t, store, id)
	}

	store.mu.Lock()
	state := store.autostartRestarts[id]
	store.mu.Unlock()
	if state == nil {
		t.Fatal("crash-looping run lost its restart state")
	}
	supervisor := findRun(t, store, id).AutostartSupervisor
	if supervisor == nil {
		t.Fatal("crash-looping run lost its durable supervisor state")
	}
	if supervisor.RestartAttempts > autostartRestartMaxAttempts {
		t.Fatalf("attempts = %d, exceeded cap %d", supervisor.RestartAttempts, autostartRestartMaxAttempts)
	}
	if !supervisor.Abandoned {
		t.Fatal("a run crashing instantly never became exhausted and would restart forever")
	}
	data, err := os.ReadFile(filepath.Join(findRun(t, store, id).Workspace, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Run
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.AutostartSupervisor == nil || !persisted.AutostartSupervisor.Abandoned ||
		persisted.AutostartSupervisor.RestartAttempts != autostartRestartMaxAttempts ||
		persisted.AutostartSupervisor.LastError == "" {
		t.Fatalf("persisted supervisor state = %+v, want durable give-up details", persisted.AutostartSupervisor)
	}
}

// A phone runs at most one program. This watch previously had no per-serial
// guard, so on its first pass it resumed every historical failure in the store:
// phones running healthy work picked up a second run, and long-dead legacy runs
// came back on phones that had moved on.
func TestAutostartRestartSkipsSerialsAlreadyWorkingAndStaleHistory(t *testing.T) {
	completed := time.Now()
	stale := completed.Add(-24 * time.Hour)
	store := &Store{
		runs:              map[string]*runState{},
		autostartRestarts: map[string]*autostartRestartState{},
		devices: &mutableFakeDevices{
			devices: []node.DeviceInfo{
				{Serial: "phone-busy", State: "device", NodeID: "n"},
				{Serial: "phone-idle", State: "device", NodeID: "n"},
			},
		},
	}
	// A healthy run plus an older crash on the same phone.
	store.runs["live"] = &runState{run: &Run{
		ID: "live", Serial: "phone-busy", Status: RunStatusRunning,
		Autostart: true, AutostartCrashRestart: true, Cmd: "/bin/sh", StartedAt: completed,
	}}
	store.runs["crashed-beside-live"] = &runState{run: &Run{
		ID: "crashed-beside-live", Serial: "phone-busy", Status: RunStatusFailed,
		Autostart: true, AutostartCrashRestart: true, Cmd: "/bin/sh",
		StartedAt: completed.Add(-time.Minute), CompletedAt: &completed,
	}}
	// A long-dead run on an idle phone.
	store.runs["ancient"] = &runState{run: &Run{
		ID: "ancient", Serial: "phone-idle", Status: RunStatusFailed,
		Autostart: true, AutostartCrashRestart: true, Cmd: "/bin/sh",
		StartedAt: stale.Add(-time.Minute), CompletedAt: &stale,
	}}
	// A deliberately stopped run must not be revived by the crash watch.
	store.runs["stopped"] = &runState{run: &Run{
		ID: "stopped", Serial: "phone-idle", Status: RunStatusStopped,
		Autostart: true, AutostartCrashRestart: true, Cmd: "/bin/sh",
		StartedAt: completed.Add(-time.Minute), CompletedAt: &completed,
	}}

	store.checkAutostartRestarts()

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, id := range []string{"crashed-beside-live", "ancient", "stopped"} {
		if supervisor := store.runs[id].run.AutostartSupervisor; supervisor != nil && supervisor.RestartAttempts > 0 {
			t.Fatalf("run %s was restarted; it should have been skipped", id)
		}
	}
}

// A program that finishes on a clean exit has done the work it was configured
// for, and restarting it makes that configuration unenforceable: a run bounded
// at twenty levels was resumed every time it reached twenty, and because a run
// keeps its progress across a resume it played one more level per attempt and
// reported twenty-eight of a limit of twenty. A licensed executable that closes
// after a session declares nothing and is still resumed, which is the whole
// reason the crash watch restarts an ordinary exit at all.
func TestCrashRestartSkipsAProgramThatFinishedOnACleanExit(t *testing.T) {
	completed := time.Now()
	zero, failure := 0, 1
	store := &Store{
		runs:              map[string]*runState{},
		autostartRestarts: map[string]*autostartRestartState{},
		programs: map[string]Program{
			"framekit": {ID: "framekit", FinishesOnCleanExit: true},
			"licensed": {ID: "licensed"},
		},
		devices: &mutableFakeDevices{
			devices: []node.DeviceInfo{
				{Serial: "phone-finished", State: "device", NodeID: "n"},
				{Serial: "phone-failed", State: "device", NodeID: "n"},
				{Serial: "phone-licensed", State: "device", NodeID: "n"},
			},
		},
	}
	for _, run := range []*Run{
		{ID: "finished", ProgramID: "framekit", Serial: "phone-finished", Status: RunStatusExited, ExitCode: &zero},
		{ID: "failed", ProgramID: "framekit", Serial: "phone-failed", Status: RunStatusExited, ExitCode: &failure},
		{ID: "licensed", ProgramID: "licensed", Serial: "phone-licensed", Status: RunStatusExited, ExitCode: &zero},
	} {
		run.Autostart, run.AutostartCrashRestart, run.Cmd = true, true, "/bin/sh"
		run.StartedAt, run.CompletedAt = completed.Add(-time.Minute), &completed
		store.runs[run.ID] = &runState{run: run}
	}

	store.checkAutostartRestarts()

	store.mu.Lock()
	defer store.mu.Unlock()
	scheduled := func(id string) bool {
		supervisor := store.runs[id].run.AutostartSupervisor
		return supervisor != nil && supervisor.NextRestartAt != nil
	}
	if scheduled("finished") {
		t.Error("a run that reported it finished was scheduled for a crash restart")
	}
	if !scheduled("failed") {
		t.Error("a non-zero exit is still a crash and must be restarted")
	}
	if !scheduled("licensed") {
		t.Error("a program that does not finish on a clean exit must still be resumed")
	}
}

// A crash loop must escalate. The streak used to be cleared the moment a resumed
// run reached Running, so a run that died after a few seconds started again from
// attempt one on every pass: the backoff never grew, the cap never fired, and one
// phone was restarted every thirty seconds for an hour while every log line read
// "attempt 1/8".
func TestAutostartRestartStreakSurvivesShortLivedRuns(t *testing.T) {
	now := time.Now()
	store := &Store{
		runs:              map[string]*runState{},
		autostartRestarts: map[string]*autostartRestartState{},
		devices: &mutableFakeDevices{
			devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "n"}},
		},
	}
	run := &Run{
		ID: "run-1", Serial: "phone-1", Status: RunStatusFailed,
		Autostart: true, AutostartCrashRestart: true, Cmd: "/bin/sh",
		Workspace: t.TempDir(),
		StartedAt: now.Add(-20 * time.Second), CompletedAt: &now,
	}
	store.runs["run-1"] = &runState{run: run}

	// Observe the crash. The backoff is in the future, so nothing is resumed.
	store.checkAutostartRestarts()
	store.mu.Lock()
	state := store.autostartRestarts["run-1"]
	if state == nil {
		store.mu.Unlock()
		t.Fatal("crashed run was not tracked")
	}
	// Stand in for several restarts already spent on this loop.
	run.AutostartSupervisor.RestartAttempts = 3
	store.mu.Unlock()

	// The run comes up and is still Running when the next tick lands, which is
	// what happens in production every five seconds.
	run.Status = RunStatusRunning
	run.StartedAt = time.Now()
	store.checkAutostartRestarts()

	store.mu.Lock()
	state = store.autostartRestarts["run-1"]
	store.mu.Unlock()
	if state == nil {
		t.Fatal("a run that had only just started lost its backoff streak")
	}
	if run.AutostartSupervisor == nil || run.AutostartSupervisor.RestartAttempts != 3 {
		t.Fatalf("supervisor reset while the run was briefly up: %+v", run.AutostartSupervisor)
	}

	// Even a run beyond the former 10-minute threshold remains in the incident
	// if failures keep recurring. This is the production ~11-minute defect.
	run.StartedAt = time.Now().Add(-11 * time.Minute)
	store.checkAutostartRestarts()
	store.mu.Lock()
	retained := store.autostartRestarts["run-1"] != nil && run.AutostartSupervisor != nil
	store.mu.Unlock()
	if !retained {
		t.Fatal("an 11-minute run cleared its restart incident")
	}

	// Six failure-free hours are enough to declare recovery.
	lastFailureAt := time.Now().Add(-2 * autostartRestartRecoveryWindow)
	run.AutostartSupervisor.LastFailureAt = &lastFailureAt
	store.checkAutostartRestarts()
	store.mu.Lock()
	cleared := store.autostartRestarts["run-1"] == nil && run.AutostartSupervisor == nil
	store.mu.Unlock()
	if !cleared {
		t.Fatal("a run beyond the failure-free recovery window kept its streak")
	}
}

// A stop somebody asked for is a decision, and restarting Mast is not new
// information about it. The startup resume used to read `stopped`, which meant
// a daemon restart revived every run an operator had deliberately stopped.
func TestExplicitStopIsNotResumedOnStartup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	devices := &mutableFakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	}
	store, err := NewStore(filepath.Join(root, "programs"), devices)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown()
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "stopped runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	reconnectEnabled := true
	crashDisabled := false
	if _, err := store.UpdateRunAutostart(started[0].ID, AutostartOptions{
		Reconnect: &reconnectEnabled, CrashRestart: &crashDisabled,
	}); err != nil {
		t.Fatal(err)
	}
	// No AutostartPaused: an ordinary stop, the kind the console sends.
	if _, err := store.Stop(StopOptions{ID: started[0].ID}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, started[0].ID)

	if status := findRun(t, store, started[0].ID).Status; status != RunStatusStopped {
		t.Fatalf("stopped run status = %s, want %s", status, RunStatusStopped)
	}
	if ids := store.autostartRunIDsForStartup(); len(ids) != 0 {
		t.Fatalf("startup autostart ids = %+v, want none", ids)
	}
}

// The runs Mast itself takes down have to come back, so they must not be
// recorded the same way as a stop that was asked for.
func TestShutdownLeavesItsRunsResumable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}

	devices := &mutableFakeDevices{
		devices: []node.DeviceInfo{{Serial: "phone-1", State: "device", NodeID: "node-1"}},
	}
	store, err := NewStore(filepath.Join(root, "programs"), devices)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registerTestProgram(t, store, source, RegisterUploadOptions{
		Name:  "shutdown runner",
		Entry: Entry{Command: "/bin/sh", Args: []string{"run.sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.Start(StartOptions{ProgramID: registered.ID, Serials: []string{"phone-1"}})
	if err != nil {
		t.Fatal(err)
	}
	reconnectEnabled := true
	crashDisabled := false
	if _, err := store.UpdateRunAutostart(started[0].ID, AutostartOptions{
		Reconnect: &reconnectEnabled, CrashRestart: &crashDisabled,
	}); err != nil {
		t.Fatal(err)
	}

	store.Shutdown()

	run := findRun(t, store, started[0].ID)
	if run.Status != RunStatusLost {
		t.Fatalf("run status after shutdown = %s, want %s", run.Status, RunStatusLost)
	}
	ids := store.autostartRunIDsForStartup()
	if len(ids) != 1 || ids[0] != started[0].ID {
		t.Fatalf("startup autostart ids = %+v, want [%s]", ids, started[0].ID)
	}
}
