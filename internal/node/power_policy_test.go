package node

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/scrcpy"
	"github.com/google/go-cmp/cmp"
)

func TestDevicePowerPolicyUsesDedicatedControlOnlyScrcpySession(t *testing.T) {
	controlMessages := make(chan []byte, 1)
	fake := &fakeADB{controlMessages: controlMessages}
	n, err := NewNode("local-node", ":0", "127.0.0.1", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Close() }()
	n.adb = fake

	cfg := mastconfig.Default()
	cfg.AndroidEnabled = true
	cfg.KeepDisplayOff = true
	n.SetConfig(filepath.Join(t.TempDir(), "config.json"), cfg, nil)
	n.ObserveDeviceReady(DeviceInfo{
		Serial:   "local-123",
		Platform: PlatformAndroid,
		State:    "device",
		NodeID:   "local-node",
	}, true)

	select {
	case got := <-controlMessages:
		want := []byte{scrcpy.SetDisplayPower, 0}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("display power message mismatch (-want +got):\n%s", diff)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for display-off policy")
	}

	fake.mu.Lock()
	reverseCalls := append([]reverseCall(nil), fake.reverseCalls...)
	shellCalls := append([]shellCall(nil), fake.shellCalls...)
	stayAwakeCalls := append([]shellCall(nil), fake.shellOutputCalls...)
	fake.mu.Unlock()

	if len(shellCalls) != 1 {
		t.Fatalf("scrcpy shell calls = %+v, want one", shellCalls)
	}
	endpoint := devicePowerPolicyEndpoint()
	if len(reverseCalls) != 1 || reverseCalls[0].DeviceSocket != endpoint.deviceSocket {
		t.Fatalf("reverse calls = %+v, want dedicated socket %q", reverseCalls, endpoint.deviceSocket)
	}
	if diff := cmp.Diff(devicePowerScrcpyArgs(endpoint), shellCalls[0].Args); diff != "" {
		t.Fatalf("scrcpy args mismatch (-want +got):\n%s", diff)
	}
	scidArgument := findArgumentWithPrefix(t, shellCalls[0].Args, "scid=")
	wantSocket := "localabstract:scrcpy_" + strings.TrimPrefix(scidArgument, "scid=")
	if reverseCalls[0].DeviceSocket != wantSocket {
		t.Fatalf("reverse socket %q disagrees with launch argument %q", reverseCalls[0].DeviceSocket, scidArgument)
	}
	wantStayAwake := shellCall{
		Host:   "",
		Serial: "local-123",
		Args:   []string{"settings", "put", "global", "stay_on_while_plugged_in", "7"},
	}
	if len(stayAwakeCalls) == 0 {
		t.Fatal("stay-awake policy was not applied")
	}
	for _, call := range stayAwakeCalls {
		if !cmp.Equal(call, wantStayAwake) {
			t.Fatalf("stay-awake call mismatch (-want +got):\n%s", cmp.Diff(wantStayAwake, call))
		}
	}
}

func TestDevicePowerPolicyOptOutStillKeepsDeviceAwake(t *testing.T) {
	fake := &fakeADB{}
	n := &Node{
		ID:                  "local-node",
		ctx:                 context.Background(),
		adb:                 fake,
		devicePowerReady:    map[string]bool{"local-123": true},
		devicePowerSessions: make(map[string]*devicePowerSession),
		devicePowerStarting: make(map[string]*devicePowerAttempt),
		devicePowerRetries:  make(map[string]*devicePowerRetry),
		devicePowerFailures: make(map[string]uint),
		configReady:         true,
		configState: mastconfig.Config{
			AndroidEnabled: true,
			KeepDisplayOff: false,
		},
	}

	n.reconcileDevicePower("local-123")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.pushCalls) != 0 || len(fake.reverseCalls) != 0 || len(fake.shellCalls) != 0 {
		t.Fatalf("display policy opt-out started scrcpy: pushes=%d reverse=%d shell=%d",
			len(fake.pushCalls), len(fake.reverseCalls), len(fake.shellCalls))
	}
	want := []shellCall{{
		Host:   "",
		Serial: "local-123",
		Args:   []string{"settings", "put", "global", "stay_on_while_plugged_in", "7"},
	}}
	if diff := cmp.Diff(want, fake.shellOutputCalls); diff != "" {
		t.Fatalf("stay-awake calls mismatch (-want +got):\n%s", diff)
	}
}

// The rotation sensor is re-enabled by system_server behind the operator's
// back, so locking a phone upright once does not keep it upright. The policy
// loop that already re-asserts stay-awake every 30s re-asserts this too.
func TestDevicePowerPolicyReassertsPortraitLock(t *testing.T) {
	fake := &fakeADB{}
	n := &Node{
		ID:                  "local-node",
		ctx:                 context.Background(),
		adb:                 fake,
		devicePowerReady:    map[string]bool{"local-123": true},
		devicePowerSessions: make(map[string]*devicePowerSession),
		devicePowerStarting: make(map[string]*devicePowerAttempt),
		devicePowerRetries:  make(map[string]*devicePowerRetry),
		devicePowerFailures: make(map[string]uint),
		configReady:         true,
		configState: mastconfig.Config{
			AndroidEnabled: true,
			KeepDisplayOff: false,
			LockPortrait:   true,
		},
	}

	n.reconcileDevicePower("local-123")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	want := []shellCall{
		{Host: "", Serial: "local-123", Args: []string{"settings", "put", "global", "stay_on_while_plugged_in", "7"}},
		{Host: "", Serial: "local-123", Args: []string{"wm", "fixed-to-user-rotation", "-d", "0", "enabled"}},
		{Host: "", Serial: "local-123", Args: []string{"settings", "put", "system", "accelerometer_rotation", "0"}},
		{Host: "", Serial: "local-123", Args: []string{"settings", "put", "system", "user_rotation", "0"}},
		{Host: "", Serial: "local-123", Args: []string{"settings", "put", "global", "force_resizable_activities", "1"}},
	}
	if diff := cmp.Diff(want, fake.shellOutputCalls); diff != "" {
		t.Fatalf("policy calls mismatch (-want +got):\n%s", diff)
	}
}

// Pinning the display refuses a landscape ad its rotation, and an unresizable
// one refused is letterboxed into a band whose close button the solver cannot
// reach. The two must ship together or the phone stops earning.
func TestDevicePortraitLockForcesResizableActivities(t *testing.T) {
	fake := &fakeADB{}
	n := &Node{
		ID:          "local-node",
		ctx:         context.Background(),
		adb:         fake,
		configReady: true,
		configState: mastconfig.Config{AndroidEnabled: true, LockPortrait: true},
	}

	if err := n.assertDevicePortraitLock("local-123"); err != nil {
		t.Fatalf("assert portrait lock: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	var pinned, resizable bool
	for _, call := range fake.shellOutputCalls {
		joined := strings.Join(call.Args, " ")
		if joined == "wm fixed-to-user-rotation -d 0 enabled" {
			pinned = true
		}
		if joined == "settings put global force_resizable_activities 1" {
			resizable = true
		}
	}
	if !pinned {
		t.Fatal("portrait lock did not pin the display to user rotation")
	}
	if !resizable {
		t.Fatal("portrait lock pinned the display without forcing activities resizable")
	}
}

// A node that has not opted into portrait locking is left alone entirely.
func TestDevicePowerPolicySkipsPortraitLockWhenNotConfigured(t *testing.T) {
	fake := &fakeADB{}
	n := &Node{
		ID:                  "local-node",
		ctx:                 context.Background(),
		adb:                 fake,
		devicePowerReady:    map[string]bool{"local-123": true},
		devicePowerSessions: make(map[string]*devicePowerSession),
		devicePowerStarting: make(map[string]*devicePowerAttempt),
		devicePowerRetries:  make(map[string]*devicePowerRetry),
		devicePowerFailures: make(map[string]uint),
		configReady:         true,
		configState: mastconfig.Config{
			AndroidEnabled: true,
			KeepDisplayOff: false,
			LockPortrait:   false,
		},
	}

	n.reconcileDevicePower("local-123")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	want := []shellCall{
		{Host: "", Serial: "local-123", Args: []string{"settings", "put", "global", "stay_on_while_plugged_in", "7"}},
	}
	if diff := cmp.Diff(want, fake.shellOutputCalls); diff != "" {
		t.Fatalf("portrait lock ran without lock_portrait (-want +got):\n%s", diff)
	}
}

func TestDevicePowerPolicySCIDHasOneHexSourceOfTruth(t *testing.T) {
	endpoint := devicePowerPolicyEndpoint()
	args := devicePowerScrcpyArgs(endpoint)
	scidArgument := findArgumentWithPrefix(t, args, "scid=")
	scid := strings.TrimPrefix(scidArgument, "scid=")

	if scid != "4d415354" {
		t.Fatalf("scid = %q, want 8-digit lowercase hexadecimal", scid)
	}
	if got, want := endpoint.deviceSocket, "localabstract:scrcpy_"+scid; got != want {
		t.Fatalf("device socket = %q, want %q derived from %q", got, want, scidArgument)
	}
}

func TestDevicePowerScrcpyArgsMatchVersion4ControlOnlySession(t *testing.T) {
	endpoint := devicePowerPolicyEndpoint()
	want := []string{
		"CLASSPATH=" + scrcpy.RemotePath,
		"app_process",
		"/",
		"com.genymobile.scrcpy.Server",
		"4.0",
		"scid=4d415354",
		"video=false",
		"audio=false",
		"control=true",
		"stay_awake=false",
		"power_on=false",
		"cleanup=false",
		"clipboard_autosync=false",
		"send_device_meta=false",
	}
	if diff := cmp.Diff(want, devicePowerScrcpyArgs(endpoint)); diff != "" {
		t.Fatalf("scrcpy v4.0 policy args mismatch (-want +got):\n%s", diff)
	}
}

func TestDevicePowerPolicyFailureHasSingleBackedOffRetry(t *testing.T) {
	fake := &fakeADB{reverseErr: errors.New("reverse failed")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &Node{
		ID:                  "local-node",
		ctx:                 ctx,
		adb:                 fake,
		devicePowerReady:    map[string]bool{"local-123": true},
		devicePowerSessions: make(map[string]*devicePowerSession),
		devicePowerStarting: make(map[string]*devicePowerAttempt),
		devicePowerRetries:  make(map[string]*devicePowerRetry),
		devicePowerFailures: make(map[string]uint),
		configReady:         true,
		configState: mastconfig.Config{
			AndroidEnabled: true,
			KeepDisplayOff: true,
		},
	}

	n.reconcileDevicePower("local-123")
	for range 10 {
		n.reconcileDevicePower("local-123")
	}

	fake.mu.Lock()
	reverseCalls := len(fake.reverseCalls)
	fake.mu.Unlock()
	if reverseCalls != 1 {
		t.Fatalf("reverse calls = %d, want one while retry is backed off", reverseCalls)
	}
	n.devicePowerMu.Lock()
	retry := n.devicePowerRetries["local-123"]
	failures := n.devicePowerFailures["local-123"]
	n.devicePowerMu.Unlock()
	if retry == nil || failures != 1 {
		t.Fatalf("retry = %v, failures = %d, want one pending retry after first failure", retry != nil, failures)
	}
	if got := devicePowerRetryDelay(failures); got != 5*time.Second {
		t.Fatalf("first retry delay = %s, want 5s", got)
	}
	if got := devicePowerRetryDelay(5); got != time.Minute {
		t.Fatalf("fifth retry delay = %s, want 1m cap", got)
	}

	cfg := mastconfig.Default()
	cfg.AndroidEnabled = true
	cfg.KeepDisplayOff = false
	n.SetConfig(filepath.Join(t.TempDir(), "config.json"), cfg, nil)
	n.devicePowerMu.Lock()
	pendingRetries := len(n.devicePowerRetries)
	pendingFailures := len(n.devicePowerFailures)
	n.devicePowerMu.Unlock()
	if pendingRetries != 0 || pendingFailures != 0 {
		t.Fatalf("opt-out left retry state armed: retries=%d failures=%d", pendingRetries, pendingFailures)
	}
}

func TestDevicePowerPolicyOptOutCancelsInFlightLaunchAndRetry(t *testing.T) {
	fake := &fakeADB{disableScrcpyConnect: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := mastconfig.Default()
	cfg.AndroidEnabled = true
	cfg.KeepDisplayOff = true
	n := &Node{
		ID:                  "local-node",
		ctx:                 ctx,
		adb:                 fake,
		devicePowerReady:    map[string]bool{"local-123": true},
		devicePowerSessions: make(map[string]*devicePowerSession),
		devicePowerStarting: make(map[string]*devicePowerAttempt),
		devicePowerRetries:  make(map[string]*devicePowerRetry),
		devicePowerFailures: make(map[string]uint),
		configPath:          configPath,
		configReady:         true,
		configState:         cfg,
	}

	done := make(chan struct{})
	go func() {
		n.reconcileDevicePower("local-123")
		close(done)
	}()
	waitForFakeScrcpyShellCall(t, fake)

	result, err := n.UpdateNodeConfig(ctx, "local-node", map[string]string{"keep_display_off": "false"})
	if err != nil {
		t.Fatalf("disable keep_display_off: %v", err)
	}
	if result.Config.KeepDisplayOff {
		t.Fatal("runtime update left keep_display_off enabled")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("in-flight policy launch did not stop after keep_display_off=false")
	}
	n.devicePowerMu.Lock()
	starting := len(n.devicePowerStarting)
	retries := len(n.devicePowerRetries)
	sessions := len(n.devicePowerSessions)
	n.devicePowerMu.Unlock()
	if starting != 0 || retries != 0 || sessions != 0 {
		t.Fatalf("policy not disarmed: starting=%d retries=%d sessions=%d", starting, retries, sessions)
	}

	fake.mu.Lock()
	shellCalls := len(fake.shellCalls)
	fake.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.shellCalls) != shellCalls {
		t.Fatalf("policy retried after opt-out: shell calls changed from %d to %d", shellCalls, len(fake.shellCalls))
	}
}

func findArgumentWithPrefix(t *testing.T, args []string, prefix string) string {
	t.Helper()
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return arg
		}
	}
	t.Fatalf("args %q contain no %q argument", args, prefix)
	return ""
}

func waitForFakeScrcpyShellCall(t *testing.T, fake *fakeADB) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		calls := len(fake.shellCalls)
		fake.mu.Unlock()
		if calls > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for scrcpy shell call")
}

func TestDevicePowerPolicyIgnoresPeerAndIOSReadiness(t *testing.T) {
	n := &Node{
		ID:               "local-node",
		devicePowerReady: make(map[string]bool),
	}
	n.ObserveDeviceReady(DeviceInfo{
		Serial:   "peer-android",
		Platform: PlatformAndroid,
		NodeID:   "peer-node",
	}, true)
	n.ObserveDeviceReady(DeviceInfo{
		Serial:   "local-ios",
		Platform: PlatformIOS,
		NodeID:   "local-node",
	}, true)

	if len(n.devicePowerReady) != 0 {
		t.Fatalf("devicePowerReady = %+v, want no peer or iOS devices", n.devicePowerReady)
	}
}
