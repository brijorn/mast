package node

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDeviceDNSReturnsAutomaticStatus(t *testing.T) {
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nlocal-123\tdevice\n"),
		},
		shellCommandOutputs: map[string][]byte{
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_mode"):      []byte("opportunistic\n"),
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_specifier"): []byte("null\n"),
		},
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	got, err := node.DeviceDNS("local-123")
	if err != nil {
		t.Fatalf("DeviceDNS returned error: %v", err)
	}

	expected := &DeviceDNSStatus{
		Mode:      "opportunistic",
		Automatic: true,
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("dns status mismatch (-want +got):\n%s", diff)
	}
}

func TestToggleDeviceDNSAutomaticSetsAdGuard(t *testing.T) {
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nlocal-123\tdevice\n"),
		},
		shellCommandOutputQueues: map[string][][]byte{
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_mode"): [][]byte{
				[]byte("opportunistic\n"),
				[]byte("hostname\n"),
			},
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_specifier"): [][]byte{
				[]byte("null\n"),
				[]byte("dns.adguard.com\n"),
			},
		},
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	got, err := node.ToggleDeviceDNS("local-123")
	if err != nil {
		t.Fatalf("ToggleDeviceDNS returned error: %v", err)
	}

	expected := &DeviceDNSStatus{
		Mode:      "hostname",
		Hostname:  "dns.adguard.com",
		Automatic: false,
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("dns status mismatch (-want +got):\n%s", diff)
	}

	expectedCalls := []shellCall{
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_mode"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_specifier"}},
		{Serial: "local-123", Args: []string{"settings", "put", "global", "private_dns_mode", "hostname"}},
		{Serial: "local-123", Args: []string{"settings", "put", "global", "private_dns_specifier", "dns.adguard.com"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_mode"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_specifier"}},
	}
	if diff := cmp.Diff(expectedCalls, settingsShellCalls(fake.shellOutputCalls)); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
	}
}

func TestToggleDeviceDNSAdGuardSetsOff(t *testing.T) {
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nlocal-123\tdevice\n"),
		},
		shellCommandOutputQueues: map[string][][]byte{
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_mode"): [][]byte{
				[]byte("hostname\n"),
				[]byte("off\n"),
			},
			shellCommandKey("local-123", "settings", "get", "global", "private_dns_specifier"): [][]byte{
				[]byte("dns.adguard.com\n"),
				[]byte("null\n"),
			},
		},
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	got, err := node.ToggleDeviceDNS("local-123")
	if err != nil {
		t.Fatalf("ToggleDeviceDNS returned error: %v", err)
	}

	expected := &DeviceDNSStatus{
		Mode: "off",
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("dns status mismatch (-want +got):\n%s", diff)
	}

	expectedCalls := []shellCall{
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_mode"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_specifier"}},
		{Serial: "local-123", Args: []string{"settings", "put", "global", "private_dns_mode", "off"}},
		{Serial: "local-123", Args: []string{"settings", "delete", "global", "private_dns_specifier"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_mode"}},
		{Serial: "local-123", Args: []string{"settings", "get", "global", "private_dns_specifier"}},
	}
	if diff := cmp.Diff(expectedCalls, settingsShellCalls(fake.shellOutputCalls)); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
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
