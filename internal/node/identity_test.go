package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func getpropKey(address string) string {
	return shellCommandKey(address, "getprop", "ro.serialno")
}

func identityNode(t *testing.T, adbOutput string, serials map[string]string) (*Node, *fakeADB) {
	t.Helper()

	outputs := make(map[string][]byte, len(serials))
	for address, serial := range serials {
		outputs[getpropKey(address)] = []byte(serial + "\n")
	}
	fake := &fakeADB{
		outputs:             map[string][]byte{"": []byte(adbOutput)},
		shellCommandOutputs: outputs,
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
		identityPath:   filepath.Join(t.TempDir(), deviceIdentityFileName),
	}
	return node, fake
}

func TestListDevicesReportsHardwareSerialForWirelessDevice(t *testing.T) {
	node, _ := identityNode(t,
		"List of devices attached\n192.168.1.159:43497\tdevice\n",
		map[string]string{"192.168.1.159:43497": "RZCY82CQMFE"},
	)

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{{
		Serial:   "RZCY82CQMFE",
		Address:  "192.168.1.159:43497",
		Platform: PlatformAndroid,
		State:    "device",
		NodeID:   "local-node",
	}}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}
}

// The whole point of the change: the same phone reached on a different
// ephemeral port is still the same device, so Runway's durable rows keep
// matching it.
func TestWirelessReconnectOnNewPortKeepsSerial(t *testing.T) {
	node, fake := identityNode(t,
		"List of devices attached\n192.168.1.159:43497\tdevice\n",
		map[string]string{
			"192.168.1.159:43497": "RZCY82CQMFE",
			"192.168.1.159:51122": "RZCY82CQMFE",
		},
	)

	before, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	fake.mu.Lock()
	fake.outputs[""] = []byte("List of devices attached\n192.168.1.159:51122\tdevice\n")
	fake.mu.Unlock()

	after, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	if before[0].Serial != after[0].Serial {
		t.Fatalf("serial changed across reconnect: %q then %q", before[0].Serial, after[0].Serial)
	}
	if after[0].Address != "192.168.1.159:51122" {
		t.Fatalf("address = %q, want the new transport", after[0].Address)
	}
}

// A device Mast cannot identify must not be reported at all. Listing it under
// its transport is what lets Runway write durable state keyed on a string that
// expires at the next reconnect.
func TestListDevicesOmitsUnidentifiedWirelessDevice(t *testing.T) {
	node, _ := identityNode(t,
		"List of devices attached\n192.168.1.159:43497\toffline\nlocal-123\tdevice\n",
		nil,
	)

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{{
		Serial:   "local-123",
		Address:  "local-123",
		Platform: PlatformAndroid,
		State:    "device",
		NodeID:   "local-node",
	}}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}
}

// An offline phone cannot answer getprop, and that is exactly when it most
// needs to keep its identity rather than vanish off its tile.
func TestOfflineWirelessDeviceKeepsCachedIdentity(t *testing.T) {
	node, fake := identityNode(t,
		"List of devices attached\n192.168.1.159:43497\tdevice\n",
		map[string]string{"192.168.1.159:43497": "RZCY82CQMFE"},
	)

	if _, err := node.ListDevices(); err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	fake.mu.Lock()
	fake.outputs[""] = []byte("List of devices attached\n192.168.1.159:43497\toffline\n")
	fake.mu.Unlock()

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}
	if len(got) != 1 || got[0].Serial != "RZCY82CQMFE" {
		t.Fatalf("offline device = %+v, want it kept under its hardware serial", got)
	}
	if got[0].State != "offline" {
		t.Fatalf("state = %q, want offline preserved", got[0].State)
	}
}

// The device list is polled, so a cache hit must not cost an adb round trip.
func TestResolvedIdentityIsCachedWithinTTL(t *testing.T) {
	node, fake := identityNode(t,
		"List of devices attached\n192.168.1.159:43497\tdevice\n",
		map[string]string{"192.168.1.159:43497": "RZCY82CQMFE"},
	)

	for range 3 {
		if _, err := node.ListDevices(); err != nil {
			t.Fatalf("ListDevices returned error: %v", err)
		}
	}

	if got := countGetpropCalls(fake); got != 1 {
		t.Fatalf("getprop calls = %d, want 1 across three listings", got)
	}
}

// A recycled address must correct itself instead of impersonating the phone
// that used to answer there.
func TestStaleIdentityIsRefreshedAfterTTL(t *testing.T) {
	node, fake := identityNode(t,
		"List of devices attached\n192.168.1.159:43497\tdevice\n",
		map[string]string{"192.168.1.159:43497": "RZCY82CQMFE"},
	)

	if _, err := node.ListDevices(); err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	node.identityMu.Lock()
	node.identityCache["192.168.1.159:43497"] = deviceIdentityEntry{
		Serial:     "RZCY82CQMFE",
		ResolvedAt: time.Now().Add(-2 * deviceIdentityTTL),
	}
	node.identityMu.Unlock()

	fake.mu.Lock()
	fake.shellCommandOutputs[getpropKey("192.168.1.159:43497")] = []byte("R5CY54NRHFK\n")
	fake.mu.Unlock()

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}
	if got[0].Serial != "R5CY54NRHFK" {
		t.Fatalf("serial = %q, want the phone now answering at that address", got[0].Serial)
	}
}

// A USB transport is already the hardware serial; probing it would buy nothing.
func TestUSBDeviceIsNotProbed(t *testing.T) {
	node, fake := identityNode(t, "List of devices attached\nlocal-123\tdevice\n", nil)

	if _, err := node.ListDevices(); err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}
	if got := countGetpropCalls(fake); got != 0 {
		t.Fatalf("getprop calls = %d, want none for a USB device", got)
	}
}

// An emulator's ro.serialno can repeat across machines, so its address stays
// its identity rather than merging two emulators into one device.
func TestNodeLocalTransportKeepsAddressAsIdentity(t *testing.T) {
	node, fake := identityNode(t,
		"List of devices attached\n127.0.0.1:5555\tdevice\nemulator-5554\tdevice\n",
		map[string]string{"127.0.0.1:5555": "EMULATOR34X3X14X0"},
	)

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("devices = %d, want both node-local devices listed", len(got))
	}
	for _, device := range got {
		if device.Serial != device.Address {
			t.Fatalf("device %+v: node-local identity should stay its address", device)
		}
	}
	if calls := countGetpropCalls(fake); calls != 0 {
		t.Fatalf("getprop calls = %d, want none for node-local transports", calls)
	}
}

// Control has to reach the phone: Runway names the hardware serial, adb needs
// the transport.
func TestControlCallsDialTheTransportAddress(t *testing.T) {
	node, fake := identityNode(t,
		"List of devices attached\n192.168.1.159:43497\tdevice\n",
		map[string]string{"192.168.1.159:43497": "RZCY82CQMFE"},
	)

	if _, err := node.ListDevices(); err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}
	if _, err := node.adbShell(node.ctx, "", "RZCY82CQMFE", "input", "tap", "1", "2"); err != nil {
		t.Fatalf("adbShell returned error: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	last := fake.shellOutputCalls[len(fake.shellOutputCalls)-1]
	if last.Serial != "192.168.1.159:43497" {
		t.Fatalf("adb dialled %q, want the transport address", last.Serial)
	}
}

// An unknown serial dials itself, which is right for every USB device and safe
// for anything not yet listed.
func TestDeviceAddressFallsBackToSerial(t *testing.T) {
	node := &Node{}
	if got := node.deviceAddress("local-123"); got != "local-123" {
		t.Fatalf("deviceAddress = %q, want the serial itself", got)
	}
}

// The mapping has to outlive a restart: a phone that is unreachable when Mast
// starts cannot be probed, and the file is the only thing that still knows it.
func TestDeviceIdentitiesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, deviceIdentityFileName)

	node, _ := identityNode(t,
		"List of devices attached\n192.168.1.159:43497\tdevice\n",
		map[string]string{"192.168.1.159:43497": "RZCY82CQMFE"},
	)
	node.identityPath = path
	if _, err := node.ListDevices(); err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("identity file not written: %v", err)
	}

	restarted := &fakeADB{
		outputs: map[string][]byte{"": []byte("List of devices attached\n192.168.1.159:43497\toffline\n")},
	}
	next := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            restarted,
	}
	next.setDeviceIdentityPath(filepath.Join(dir, "config.json"))

	got, err := next.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}
	if len(got) != 1 || got[0].Serial != "RZCY82CQMFE" {
		t.Fatalf("after restart = %+v, want the persisted identity", got)
	}
}

func TestSanitizeDeviceSerialRejectsUnusableValues(t *testing.T) {
	cases := map[string]string{
		"RZCY82CQMFE\n":        "RZCY82CQMFE",
		"  RZCY82CQMFE  \r\n":  "RZCY82CQMFE",
		"":                     "",
		"unknown\n":            "",
		"null":                 "",
		"192.168.1.159:43497":  "",
		"RZCY82CQMFE\nextra\n": "RZCY82CQMFE",
	}
	for raw, want := range cases {
		if got := sanitizeDeviceSerial(raw); got != want {
			t.Fatalf("sanitizeDeviceSerial(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestIsNodeLocalTransport(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5555":      true,
		"emulator-5554":       true,
		"localhost:5555":      true,
		"[::1]:5555":          true,
		"192.168.1.159:43497": false,
		"RZCY82CQMFE":         false,
	}
	for transport, want := range cases {
		if got := isNodeLocalTransport(transport); got != want {
			t.Fatalf("isNodeLocalTransport(%q) = %v, want %v", transport, got, want)
		}
	}
}

func countGetpropCalls(fake *fakeADB) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	count := 0
	for _, call := range fake.shellOutputCalls {
		if len(call.Args) == 2 && call.Args[0] == "getprop" && call.Args[1] == "ro.serialno" {
			count++
		}
	}
	return count
}
