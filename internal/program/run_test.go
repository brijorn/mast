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
	after := findRun(t, store, resumed.ID)
	if after.Env["MAX_LEVELS"] != "1" {
		t.Fatalf("stored MAX_LEVELS = %q, want original starting value 1", after.Env["MAX_LEVELS"])
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
	if _, err := store.SetRunAutostart(started[0].ID, true); err != nil {
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
}
