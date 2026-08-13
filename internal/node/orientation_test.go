package node

import (
	"strings"
	"testing"

	"github.com/brijorn/ioslink"
	"github.com/google/go-cmp/cmp"
)

func TestSetDeviceOrientationLandscape(t *testing.T) {
	fake := &fakeADB{outputs: map[string][]byte{
		"": []byte("List of devices attached\nlocal-123\tdevice\n"),
	}}

	got, err := dnsTestNode(fake).SetDeviceOrientation("local-123", DeviceOrientationLandscape)
	if err != nil {
		t.Fatalf("SetDeviceOrientation returned error: %v", err)
	}
	want := &DeviceOrientationStatus{
		Serial:      "local-123",
		Platform:    PlatformAndroid,
		Orientation: DeviceOrientationLandscape,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("orientation status mismatch (-want +got):\n%s", diff)
	}
	wantCalls := []shellCall{
		{Serial: "local-123", Args: []string{"wm", "fixed-to-user-rotation", "-d", "0", "enabled"}},
		{Serial: "local-123", Args: []string{"settings", "put", "system", "accelerometer_rotation", "0"}},
		{Serial: "local-123", Args: []string{"settings", "put", "system", "user_rotation", "1"}},
	}
	if diff := cmp.Diff(wantCalls, fake.shellOutputCalls); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
	}
}

// The policy loop re-asserts portrait every thirty seconds. An operator who
// turned this handset must not have it turned back under them.
func TestSetDeviceOrientationSurvivesThePolicyLoop(t *testing.T) {
	fake := &fakeADB{outputs: map[string][]byte{
		"": []byte("List of devices attached\nlocal-123\tdevice\n"),
	}}
	n := dnsTestNode(fake)
	n.configMu.Lock()
	n.configReady = true
	n.configState.AndroidEnabled = true
	n.configState.LockPortrait = true
	n.configMu.Unlock()

	if _, err := n.SetDeviceOrientation("local-123", DeviceOrientationLandscape); err != nil {
		t.Fatalf("SetDeviceOrientation: %v", err)
	}
	fake.mu.Lock()
	fake.shellOutputCalls = nil
	fake.mu.Unlock()

	if err := n.assertDevicePortraitLock("local-123"); err != nil {
		t.Fatalf("assert portrait lock: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, call := range fake.shellOutputCalls {
		if strings.Join(call.Args, " ") == "settings put system user_rotation 0" {
			t.Fatal("policy loop overwrote the operator's landscape with portrait")
		}
	}
}

func TestSetDeviceOrientationRejectsInvalidValue(t *testing.T) {
	fake := &fakeADB{outputs: map[string][]byte{
		"": []byte("List of devices attached\nlocal-123\tdevice\n"),
	}}
	_, err := dnsTestNode(fake).SetDeviceOrientation("local-123", DeviceOrientation("sideways"))
	if err == nil || !strings.Contains(err.Error(), "unsupported device orientation") {
		t.Fatalf("SetDeviceOrientation error = %v, want invalid orientation error", err)
	}
	if len(fake.shellOutputCalls) != 0 {
		t.Fatalf("ADB shell calls = %+v, want none", fake.shellOutputCalls)
	}
}

func TestSetDeviceOrientationRejectsIOSBeforeADB(t *testing.T) {
	originalListIOSDevices := listIOSDevices
	listIOSDevices = func() ([]ioslink.DeviceSummary, error) {
		return []ioslink.DeviceSummary{{UDID: "ios-1", State: "device"}}, nil
	}
	defer func() { listIOSDevices = originalListIOSDevices }()

	fake := &fakeADB{outputs: map[string][]byte{"": []byte("List of devices attached\n")}}
	node := dnsTestNode(fake)
	node.IOSEnabled = true
	_, err := node.SetDeviceOrientation("ios-1", DeviceOrientationLandscape)
	if err == nil || !strings.Contains(err.Error(), "not supported for iOS") {
		t.Fatalf("SetDeviceOrientation error = %v, want unsupported iOS error", err)
	}
	if len(fake.shellOutputCalls) != 0 {
		t.Fatalf("ADB shell calls = %+v, want none", fake.shellOutputCalls)
	}
}

func TestSetDeviceOrientationRoutesToOwningPeer(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	nodeA.adb = &fakeADB{outputs: map[string][]byte{
		"": []byte("List of devices attached\n"),
	}}
	remoteADB := &fakeADB{outputs: map[string][]byte{
		"": []byte("List of devices attached\nremote-123\tdevice\n"),
	}}
	nodeB.adb = remoteADB
	nodeB.AndroidEnabled = true
	connectNodePair(t, nodeA, nodeB)

	got, err := nodeA.SetDeviceOrientation("remote-123", DeviceOrientationPortrait)
	if err != nil {
		t.Fatalf("SetDeviceOrientation returned error: %v", err)
	}
	if got.Serial != "remote-123" || got.Orientation != DeviceOrientationPortrait {
		t.Fatalf("orientation status = %+v, want remote-123 portrait", got)
	}
	wantRotation := shellCall{
		Serial: "remote-123",
		Args:   []string{"settings", "put", "system", "user_rotation", "0"},
	}
	var rotationCall *shellCall
	for _, call := range remoteADB.shellOutputCallsSnapshot() {
		if cmp.Equal(call, wantRotation) {
			callCopy := call
			rotationCall = &callCopy
			break
		}
	}
	if rotationCall == nil {
		t.Fatalf("peer shell calls = %+v, want user_rotation portrait command", remoteADB.shellOutputCallsSnapshot())
	}
}
