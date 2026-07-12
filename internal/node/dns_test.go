package node

import (
	"strings"
	"testing"

	"github.com/brijorn/ioslink"
	"github.com/google/go-cmp/cmp"
)

func TestSetDeviceDNSRejectsIOSBeforeADB(t *testing.T) {
	originalListIOSDevices := listIOSDevices
	listIOSDevices = func() ([]ioslink.DeviceSummary, error) {
		return []ioslink.DeviceSummary{{UDID: "ios-1", State: "device"}}, nil
	}
	defer func() { listIOSDevices = originalListIOSDevices }()

	fake := &fakeADB{outputs: map[string][]byte{"": []byte("List of devices attached\n")}}
	node := dnsTestNode(fake)
	node.IOSEnabled = true
	_, err := node.SetDeviceDNS("ios-1", DeviceDNSStatus{Mode: DeviceDNSModeOff})
	if err == nil || !strings.Contains(err.Error(), "not supported for iOS") {
		t.Fatalf("SetDeviceDNS error = %v, want unsupported iOS error", err)
	}
	if len(fake.shellOutputCalls) != 0 {
		t.Fatalf("ADB shell calls = %+v, want none", fake.shellOutputCalls)
	}
}

func dnsTestNode(fake *fakeADB) *Node {
	return &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}
}

func TestDeviceDNSNormalizesAndroidAutomaticMode(t *testing.T) {
	fake := &fakeADB{
		outputs: map[string][]byte{"": []byte("List of devices attached\nlocal-123\tdevice\n")},
		shellCommandOutputs: map[string][]byte{
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_mode"):      []byte("opportunistic\n"),
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_specifier"): []byte("null\n"),
		},
	}

	got, err := dnsTestNode(fake).DeviceDNS("local-123")
	if err != nil {
		t.Fatalf("DeviceDNS returned error: %v", err)
	}
	want := &DeviceDNSStatus{Mode: DeviceDNSModeAutomatic}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("dns status mismatch (-want +got):\n%s", diff)
	}
}

func TestSetDeviceDNSHostname(t *testing.T) {
	fake := &fakeADB{
		outputs: map[string][]byte{"": []byte("List of devices attached\nlocal-123\tdevice\n")},
		shellCommandOutputs: map[string][]byte{
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_mode"):      []byte("hostname\n"),
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_specifier"): []byte("dns.adguard.com\n"),
		},
	}

	got, err := dnsTestNode(fake).SetDeviceDNS("local-123", DeviceDNSStatus{
		Mode: DeviceDNSModeHostname, Hostname: "dns.adguard.com",
	})
	if err != nil {
		t.Fatalf("SetDeviceDNS returned error: %v", err)
	}
	want := &DeviceDNSStatus{Mode: DeviceDNSModeHostname, Hostname: "dns.adguard.com"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("dns status mismatch (-want +got):\n%s", diff)
	}
	wantCalls := []shellCall{
		{Serial: "local-123", Args: []string{"settings", "put", "global", "private_dns_mode", "hostname"}},
		{Serial: "local-123", Args: []string{"settings", "put", "global", "private_dns_specifier", "dns.adguard.com"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_mode"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_specifier"}},
	}
	if diff := cmp.Diff(wantCalls, settingsShellCalls(fake.shellOutputCalls)); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
	}
}

func TestSetDeviceDNSOff(t *testing.T) {
	fake := &fakeADB{
		outputs: map[string][]byte{"": []byte("List of devices attached\nlocal-123\tdevice\n")},
		shellCommandOutputs: map[string][]byte{
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_mode"):      []byte("off\n"),
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_specifier"): []byte("null\n"),
		},
	}

	got, err := dnsTestNode(fake).SetDeviceDNS("local-123", DeviceDNSStatus{Mode: DeviceDNSModeOff})
	if err != nil {
		t.Fatalf("SetDeviceDNS returned error: %v", err)
	}
	want := &DeviceDNSStatus{Mode: DeviceDNSModeOff}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("dns status mismatch (-want +got):\n%s", diff)
	}
	wantCalls := []shellCall{
		{Serial: "local-123", Args: []string{"settings", "put", "global", "private_dns_mode", "off"}},
		{Serial: "local-123", Args: []string{"settings", "delete", "global", "private_dns_specifier"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_mode"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_specifier"}},
	}
	if diff := cmp.Diff(wantCalls, settingsShellCalls(fake.shellOutputCalls)); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
	}
}

func TestSetDeviceDNSHostnameRequiresHostname(t *testing.T) {
	fake := &fakeADB{outputs: map[string][]byte{"": []byte("List of devices attached\nlocal-123\tdevice\n")}}
	_, err := dnsTestNode(fake).SetDeviceDNS("local-123", DeviceDNSStatus{Mode: DeviceDNSModeHostname})
	if err == nil {
		t.Fatal("SetDeviceDNS returned nil error, want hostname validation error")
	}
}

func settingsShellCalls(calls []shellCall) []shellCall {
	var settingsCalls []shellCall
	for _, call := range calls {
		if len(call.Args) == 0 || call.Args[0] != "settings" {
			continue
		}
		settingsCalls = append(settingsCalls, call)
	}
	return settingsCalls
}
