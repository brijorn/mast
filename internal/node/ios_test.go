package node

import (
	"strings"
	"testing"

	"github.com/brijorn/ioslink"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/google/go-cmp/cmp"
)

func TestIOSDeviceInfosDeduplicatesAndPrefersUSB(t *testing.T) {
	summaries := []ioslink.DeviceSummary{
		{UDID: "ios-1", State: "network", ConnectionType: "Network"},
		{UDID: "ios-2", State: "device", ConnectionType: "USB"},
		{UDID: "ios-1", State: "device", ConnectionType: "USB"},
		{UDID: "ios-2", State: "network", ConnectionType: "Network"},
	}

	got := iosDeviceInfos(summaries, "node-a")
	want := []DeviceInfo{
		{Serial: "ios-1", Platform: PlatformIOS, State: "device", NodeID: "node-a"},
		{Serial: "ios-2", Platform: PlatformIOS, State: "device", NodeID: "node-a"},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("iOS devices mismatch (-want +got):\n%s", diff)
	}
}

func TestStartLocalStreamRejectsIOSBeforeScrcpyPush(t *testing.T) {
	originalListIOSDevices := listIOSDevices
	listIOSDevices = func() ([]ioslink.DeviceSummary, error) {
		return []ioslink.DeviceSummary{
			{UDID: "ios-1", State: "device", ConnectionType: "USB"},
		}, nil
	}
	t.Cleanup(func() {
		listIOSDevices = originalListIOSDevices
	})

	fake := &fakeADB{}
	node := &Node{
		ID:         "node-a",
		IOSEnabled: true,
		adb:        fake,
	}

	_, err := node.startLocalAndroidStream("ios-1", streamcfg.Options{})
	if err == nil || !strings.Contains(err.Error(), "not android") {
		t.Fatalf("startLocalAndroidStream error = %v, want not android", err)
	}
	if len(fake.pushCalls) != 0 {
		t.Fatalf("ADB push calls = %d, want 0", len(fake.pushCalls))
	}
}

func TestEnsureLocalStreamUsesExistingIOSStreamWithoutAndroidFallback(t *testing.T) {
	originalListIOSDevices := listIOSDevices
	listIOSDevices = func() ([]ioslink.DeviceSummary, error) {
		return []ioslink.DeviceSummary{
			{UDID: "ios-1", State: "device", ConnectionType: "USB"},
		}, nil
	}
	t.Cleanup(func() {
		listIOSDevices = originalListIOSDevices
	})

	done := make(chan struct{})
	close(done)
	session := &StreamSession{
		ID:           "ios-stream",
		DeviceSerial: "ios-1",
		Platform:     PlatformIOS,
		Kind:         "mjpeg",
	}
	fake := &fakeADB{}
	node := &Node{
		ID:         "node-a",
		IOSEnabled: true,
		adb:        fake,
		streams: map[string]*streamEntry{
			"ios-1": {Session: session, Done: done},
		},
	}

	got, err := node.ensureLocalStream("ios-1", streamcfg.Options{})
	if err != nil {
		t.Fatalf("ensureLocalStream returned error: %v", err)
	}
	if got != session {
		t.Fatal("ensureLocalStream did not return the existing iOS stream")
	}
	if len(fake.pushCalls) != 0 {
		t.Fatalf("ADB push calls = %d, want 0", len(fake.pushCalls))
	}
	if len(fake.reverseCalls) != 0 {
		t.Fatalf("ADB reverse calls = %d, want 0", len(fake.reverseCalls))
	}
	if len(fake.shellCalls) != 0 {
		t.Fatalf("ADB shell calls = %d, want 0", len(fake.shellCalls))
	}
}
