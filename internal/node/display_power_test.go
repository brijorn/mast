package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/scrcpy"
	"github.com/google/go-cmp/cmp"
)

const displayPowerTestSerial = "local-123"

func displayPowerTestNode(t *testing.T, fake *fakeADB, keepDisplayOff bool) *Node {
	t.Helper()
	if fake.outputs == nil {
		fake.outputs = make(map[string][]byte)
	}
	fake.outputs[""] = []byte("List of devices attached\n" + displayPowerTestSerial + "\tdevice\n")

	n, err := NewNode("local-node", ":0", "127.0.0.1", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	n.adb = fake

	cfg := mastconfig.Default()
	cfg.AndroidEnabled = true
	cfg.KeepDisplayOff = keepDisplayOff
	n.SetConfig(filepath.Join(t.TempDir(), "config.json"), cfg, nil)
	return n
}

func awaitDisplayPowerMessage(t *testing.T, messages chan []byte, on byte, what string) {
	t.Helper()
	want := []byte{scrcpy.SetDisplayPower, on}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-messages:
			if diff := cmp.Diff(want, got); diff == "" {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// An operator turning one screen on has to beat the node policy that darkens it
// every thirty seconds, or the button appears to do nothing a moment later.
func TestDisplayPowerOverrideBeatsKeepDisplayOffPolicy(t *testing.T) {
	messages := make(chan []byte, 8)
	fake := &fakeADB{controlMessages: messages}
	n := displayPowerTestNode(t, fake, true)

	n.ObserveDeviceReady(DeviceInfo{
		Serial:   displayPowerTestSerial,
		Platform: PlatformAndroid,
		State:    "device",
		NodeID:   "local-node",
	}, true)
	awaitDisplayPowerMessage(t, messages, 0, "the policy to darken the panel")

	status, err := n.SetDeviceDisplayPower(displayPowerTestSerial, DeviceDisplayPowerOn)
	if err != nil {
		t.Fatalf("turn display on: %v", err)
	}
	awaitDisplayPowerMessage(t, messages, 1, "the override to light the panel")

	if status.Requested != DeviceDisplayPowerOn {
		t.Fatalf("requested = %q, want on", status.Requested)
	}
	if status.Panel != DeviceDisplayPowerOn {
		t.Fatalf("panel = %q, want on", status.Panel)
	}
	if status.Policy != DeviceDisplayPowerOff {
		t.Fatalf("policy = %q, want off", status.Policy)
	}

	// The reassert path is what a viewer stream and the thirty-second ticker both
	// run. It must now re-state the override, not the policy.
	n.reassertDevicePowerPolicy(displayPowerTestSerial)
	awaitDisplayPowerMessage(t, messages, 1, "the reassert to keep the panel lit")
}

// Clearing the override hands the phone back to the node policy, which is the
// only way an operator can undo a manual decision without a restart.
func TestDisplayPowerPolicyRequestRestoresNodePolicy(t *testing.T) {
	messages := make(chan []byte, 8)
	fake := &fakeADB{controlMessages: messages}
	n := displayPowerTestNode(t, fake, true)

	n.ObserveDeviceReady(DeviceInfo{
		Serial:   displayPowerTestSerial,
		Platform: PlatformAndroid,
		State:    "device",
		NodeID:   "local-node",
	}, true)
	awaitDisplayPowerMessage(t, messages, 0, "the policy to darken the panel")

	if _, err := n.SetDeviceDisplayPower(displayPowerTestSerial, DeviceDisplayPowerOn); err != nil {
		t.Fatalf("turn display on: %v", err)
	}
	awaitDisplayPowerMessage(t, messages, 1, "the override to light the panel")

	status, err := n.SetDeviceDisplayPower(displayPowerTestSerial, DeviceDisplayPowerPolicy)
	if err != nil {
		t.Fatalf("return display to policy: %v", err)
	}
	if status.Requested != DeviceDisplayPowerPolicy {
		t.Fatalf("requested = %q, want policy", status.Requested)
	}
	awaitDisplayPowerMessage(t, messages, 0, "the policy to darken the panel again")
}

// A node running no display-off policy still has to be able to darken one phone
// on request, which means building a control session where the policy would
// never have made one.
func TestDisplayPowerOffWorksWithoutKeepDisplayOffPolicy(t *testing.T) {
	messages := make(chan []byte, 8)
	fake := &fakeADB{controlMessages: messages}
	n := displayPowerTestNode(t, fake, false)

	n.ObserveDeviceReady(DeviceInfo{
		Serial:   displayPowerTestSerial,
		Platform: PlatformAndroid,
		State:    "device",
		NodeID:   "local-node",
	}, true)

	status, err := n.SetDeviceDisplayPower(displayPowerTestSerial, DeviceDisplayPowerOff)
	if err != nil {
		t.Fatalf("turn display off: %v", err)
	}
	awaitDisplayPowerMessage(t, messages, 0, "the override to darken the panel")
	if status.Panel != DeviceDisplayPowerOff {
		t.Fatalf("panel = %q, want off", status.Panel)
	}
	if status.Policy != DeviceDisplayPowerUnknown {
		t.Fatalf("policy = %q, want unknown with keep_display_off disabled", status.Policy)
	}

	// The reconcile sweep used to read "no policy" as "stop every session". The
	// overridden phone must survive it.
	n.reconcileDevicePowerPolicy()
	n.devicePowerMu.Lock()
	session := n.devicePowerSessions[displayPowerTestSerial]
	n.devicePowerMu.Unlock()
	if session == nil {
		t.Fatal("reconcile tore down the session holding an operator's phone dark")
	}
}

// Mast reports what it asserted, never a guess. A device it holds no session for
// gets "unknown", so a console drawing a button can say so instead of assuming.
func TestDisplayPowerStatusIsUnknownWithoutASession(t *testing.T) {
	fake := &fakeADB{}
	n := displayPowerTestNode(t, fake, false)

	status, err := n.DeviceDisplayPower(displayPowerTestSerial)
	if err != nil {
		t.Fatalf("read display power: %v", err)
	}
	if status.Panel != DeviceDisplayPowerUnknown {
		t.Fatalf("panel = %q, want unknown", status.Panel)
	}
	if status.Requested != DeviceDisplayPowerPolicy {
		t.Fatalf("requested = %q, want policy", status.Requested)
	}
}

// The override is one instruction about the handset in front of the operator,
// not a stored setting. A phone that left and came back returns to policy.
func TestDisplayPowerOverrideClearedWhenDeviceDisconnects(t *testing.T) {
	messages := make(chan []byte, 8)
	fake := &fakeADB{controlMessages: messages}
	n := displayPowerTestNode(t, fake, true)

	device := DeviceInfo{
		Serial:   displayPowerTestSerial,
		Platform: PlatformAndroid,
		State:    "device",
		NodeID:   "local-node",
	}
	n.ObserveDeviceReady(device, true)
	awaitDisplayPowerMessage(t, messages, 0, "the policy to darken the panel")
	if _, err := n.SetDeviceDisplayPower(displayPowerTestSerial, DeviceDisplayPowerOn); err != nil {
		t.Fatalf("turn display on: %v", err)
	}
	awaitDisplayPowerMessage(t, messages, 1, "the override to light the panel")

	n.ObserveDeviceReady(device, false)

	n.devicePowerMu.Lock()
	_, overridden := n.devicePowerOverride[displayPowerTestSerial]
	_, asserted := n.devicePowerAsserted[displayPowerTestSerial]
	n.devicePowerMu.Unlock()
	if overridden {
		t.Fatal("override outlived the device it was about")
	}
	if asserted {
		t.Fatal("asserted panel state outlived the session that produced it")
	}
}

func TestSetDeviceDisplayPowerRejectsUnknownValue(t *testing.T) {
	fake := &fakeADB{}
	n := displayPowerTestNode(t, fake, true)

	if _, err := n.SetDeviceDisplayPower(displayPowerTestSerial, DeviceDisplayPower("dim")); err == nil {
		t.Fatal("SetDeviceDisplayPower accepted an unsupported value")
	}
}

func TestDevicePowerIntentPrefersOverrideOverPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := mastconfig.Default()
	cfg.AndroidEnabled = true
	cfg.KeepDisplayOff = true
	n := &Node{
		ID:                  "local-node",
		ctx:                 ctx,
		devicePowerOverride: map[string]bool{"lit": true, "dark": false},
		configReady:         true,
		configState:         cfg,
	}

	for _, tc := range []struct {
		serial   string
		wantHold bool
		wantOn   bool
	}{
		{serial: "lit", wantHold: true, wantOn: true},
		{serial: "dark", wantHold: true, wantOn: false},
		{serial: "unmentioned", wantHold: true, wantOn: false},
	} {
		hold, on := n.devicePowerIntent(tc.serial)
		if hold != tc.wantHold || on != tc.wantOn {
			t.Fatalf("intent(%s) = (%v, %v), want (%v, %v)", tc.serial, hold, on, tc.wantHold, tc.wantOn)
		}
	}

	n.configState.KeepDisplayOff = false
	if hold, _ := n.devicePowerIntent("unmentioned"); hold {
		t.Fatal("a device with no override still wants a session with the policy off")
	}
	if hold, on := n.devicePowerIntent("dark"); !hold || on {
		t.Fatal("an operator's darkened phone stopped being held when the policy went away")
	}
}
