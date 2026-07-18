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
		{Serial: "local-123", Args: []string{"wm", "set-ignore-orientation-request", "-d", "0", "true"}},
		{Serial: "local-123", Args: []string{"settings", "put", "system", "accelerometer_rotation", "0"}},
		{Serial: "local-123", Args: []string{"settings", "put", "system", "user_rotation", "1"}},
	}
	if diff := cmp.Diff(wantCalls, fake.shellOutputCalls); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
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
