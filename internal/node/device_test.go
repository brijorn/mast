package node

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/scrcpy"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/google/go-cmp/cmp"
)

type pushCall struct {
	Host       string
	Serial     string
	LocalPath  string
	RemotePath string
}

type reverseCall struct {
	Host         string
	Serial       string
	DeviceSocket string
	LocalPort    int
}

type shellCall struct {
	Host   string
	Serial string
	Args   []string
}

type fakeADB struct {
	mu                       sync.Mutex
	outputs                  map[string][]byte
	errors                   map[string]error
	shellOutputs             map[string][]byte
	shellErrors              map[string]error
	shellCommandOutputs      map[string][]byte
	shellCommandOutputQueues map[string][][]byte
	shellCommandErrors       map[string]error
	execOutOutputs           map[string][]byte
	execOutErrors            map[string]error
	deviceWaits              map[string]<-chan struct{}
	calls                    []string
	pushCalls                []pushCall
	reverseCalls             []reverseCall
	shellCalls               []shellCall
	shellOutputCalls         []shellCall
	controlMessages          chan []byte
}

func (a *fakeADB) Devices(ctx context.Context, host string) ([]byte, error) {
	a.mu.Lock()
	a.calls = append(a.calls, host)
	wait := a.deviceWaits[host]
	err := a.errors[host]
	output := append([]byte(nil), a.outputs[host]...)
	a.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (a *fakeADB) Push(ctx context.Context, host string, serial string, localPath string, remotePath string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pushCalls = append(a.pushCalls, pushCall{
		Host:       host,
		Serial:     serial,
		LocalPath:  localPath,
		RemotePath: remotePath,
	})
	return nil
}

func (a *fakeADB) Reverse(ctx context.Context, host string, serial string, deviceSocket string, localPort int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reverseCalls = append(a.reverseCalls, reverseCall{
		Host:         host,
		Serial:       serial,
		DeviceSocket: deviceSocket,
		LocalPort:    localPort,
	})
	return nil
}

func (a *fakeADB) StartShell(host string, serial string, arg ...string) (*exec.Cmd, error) {
	a.mu.Lock()
	a.shellCalls = append(a.shellCalls, shellCall{
		Host:   host,
		Serial: serial,
		Args:   append([]string(nil), arg...),
	})
	var port int
	if len(a.reverseCalls) > 0 {
		port = a.reverseCalls[len(a.reverseCalls)-1].LocalPort
	}
	controlMessages := a.controlMessages
	a.mu.Unlock()
	if port > 0 {
		audio, control := fakeScrcpySocketOptions(arg)
		go writeFakeScrcpySockets(port, audio, control, controlMessages)
	}
	return nil, nil
}

func (a *fakeADB) Shell(ctx context.Context, host string, serial string, arg ...string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.shellOutputCalls = append(a.shellOutputCalls, shellCall{
		Host:   host,
		Serial: serial,
		Args:   append([]string(nil), arg...),
	})
	key := shellCommandKey(serial, arg...)
	if outputs := a.shellCommandOutputQueues[key]; len(outputs) > 0 {
		output := outputs[0]
		a.shellCommandOutputQueues[key] = outputs[1:]
		return output, nil
	}
	if err := a.shellCommandErrors[key]; err != nil {
		return nil, err
	}
	if output, ok := a.shellCommandOutputs[key]; ok {
		return output, nil
	}
	if err := a.shellErrors[serial]; err != nil {
		return nil, err
	}
	return a.shellOutputs[serial], nil
}

func shellCommandKey(serial string, arg ...string) string {
	return serial + "\x00" + strings.Join(arg, "\x00")
}

func (a *fakeADB) ExecOut(ctx context.Context, host string, serial string, arg ...string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.shellOutputCalls = append(a.shellOutputCalls, shellCall{
		Host:   host,
		Serial: serial,
		Args:   append([]string(nil), append([]string{"exec-out"}, arg...)...),
	})
	if err := a.execOutErrors[serial]; err != nil {
		return nil, err
	}
	return a.execOutOutputs[serial], nil
}

func (a *fakeADB) shellOutputCallsSnapshot() []shellCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := append([]shellCall(nil), a.shellOutputCalls...)
	for index := range calls {
		calls[index].Args = append([]string(nil), calls[index].Args...)
	}
	return calls
}

func fakeScrcpySocketOptions(args []string) (bool, bool) {
	audio := true
	control := true
	for _, arg := range args {
		switch arg {
		case "audio=false":
			audio = false
		case "control=false":
			control = false
		}
	}
	return audio, control
}

func writeFakeScrcpySockets(port int, audio bool, control bool, controlMessages chan<- []byte) {
	writeFakeScrcpyVideoMetadata(port)
	if audio {
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
		}
	}
	if control {
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if controlMessages == nil {
			return
		}
		message := make([]byte, 2)
		if _, err := io.ReadFull(conn, message); err == nil {
			controlMessages <- message
		}
	}
}

func writeFakeScrcpyVideoMetadata(port int) {
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	deviceName := make([]byte, 64)
	copy(deviceName, "fake-device")

	streamMeta := make([]byte, 16)
	copy(streamMeta[:4], "h264")
	binary.BigEndian.PutUint32(streamMeta[4:8], 0x80000000)
	binary.BigEndian.PutUint32(streamMeta[8:12], 944)
	binary.BigEndian.PutUint32(streamMeta[12:16], 1080)

	_, _ = conn.Write(deviceName)
	_, _ = conn.Write(streamMeta)
}

func TestCommandErrorIncludesCommandAndOutput(t *testing.T) {
	err := commandError(
		"adb",
		[]string{"reverse", "localabstract:scrcpy", "tcp:55605"},
		[]byte("more than one device/emulator\n"),
		errors.New("exit status 1"),
	)

	got := err.Error()
	for _, want := range []string{
		"adb reverse localabstract:scrcpy tcp:55605",
		"exit status 1",
		"more than one device/emulator",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("error = %q, want it to contain %q", got, want)
		}
	}
}

func TestRealADBTimeoutErrorIncludesCommandHostAndSerial(t *testing.T) {
	originalExec := execADBCommand
	originalTimeout := adbCommandTimeout
	t.Cleanup(func() {
		execADBCommand = originalExec
		adbCommandTimeout = originalTimeout
	})

	adbCommandTimeout = 10 * time.Millisecond
	execADBCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		helperArgs := []string{"-test.run=TestADBTimeoutHelperProcess", "--", name}
		helperArgs = append(helperArgs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(), "MAST_ADB_TIMEOUT_HELPER=1")
		return cmd
	}

	_, err := (realADB{}).Shell(context.Background(), "10.0.0.2", "serial-123", "settings", "get", "global", "private_dns_mode")
	if err == nil {
		t.Fatal("Shell returned nil error, want timeout")
	}
	got := err.Error()
	for _, want := range []string{
		"adb -H 10.0.0.2 -P 5037 -s serial-123 shell settings get global private_dns_mode",
		context.DeadlineExceeded.Error(),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("error = %q, want it to contain %q", got, want)
		}
	}
}

func TestADBTimeoutHelperProcess(t *testing.T) {
	if os.Getenv("MAST_ADB_TIMEOUT_HELPER") != "1" {
		return
	}
	time.Sleep(time.Hour)
}

func TestParseDevicesOutput(t *testing.T) {
	parserADBOutput := "List of devices attached\nabc123\tdevice\nxyz789\toffline\n"
	got := parseDevicesOutput(parserADBOutput, "node-a", []DeviceInfo{})

	expected := []DeviceInfo{
		{Serial: "abc123", Platform: PlatformAndroid, State: "device", NodeID: "node-a"},
		{Serial: "xyz789", Platform: PlatformAndroid, State: "offline", NodeID: "node-a"},
	}

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}
}

func TestListLocalDeviceStatesFiltersStartupBlacklist(t *testing.T) {
	n, err := NewNode("node-a", ":0", "127.0.0.1", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Close() }()
	n.adb = &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nabc123\tdevice\nxyz789\tdevice\n"),
		},
		errors: map[string]error{},
	}
	cfg := mastconfig.Default()
	cfg.AndroidEnabled = true
	cfg.DeviceBlacklist = []string{"abc123"}
	n.SetConfig("", cfg, nil)

	got, err := n.listLocalDeviceStates()
	if err != nil {
		t.Fatalf("listLocalDeviceStates returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "xyz789", Platform: PlatformAndroid, State: "device", NodeID: "node-a"},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}
	if _, err := n.DeviceBySerial("abc123"); err == nil {
		t.Fatal("DeviceBySerial returned blacklisted device, want error")
	}
}

func TestParseBatteryLevel(t *testing.T) {
	got, err := parseBatteryLevel("AC powered: false\n  level: 87\nscale: 100\n")
	if err != nil {
		t.Fatalf("parseBatteryLevel returned error: %v", err)
	}
	if got == nil || *got != 87 {
		t.Fatalf("battery level = %v, want 87", got)
	}
}

func TestParseBatteryLevelReturnsNilForUnknownOutput(t *testing.T) {
	for _, output := range []string{
		"AC powered: false\nscale: 100\n",
		"level: unknown\n",
		"level:\n",
	} {
		got, err := parseBatteryLevel(output)
		if err != nil {
			t.Fatalf("parseBatteryLevel(%q) returned error: %v", output, err)
		}
		if got != nil {
			t.Fatalf("parseBatteryLevel(%q) = %d, want nil", output, *got)
		}
	}
}

func TestParseBatterySnapshotCharging(t *testing.T) {
	got, err := parseBatterySnapshot("Current Battery Service state:\n  AC powered: false\n  USB powered: true\n  Wireless powered: false\n  Dock powered: false\n  status: 2\n  level: 87\n  current now: 120\n")
	if err != nil {
		t.Fatalf("parseBatterySnapshot returned error: %v", err)
	}
	if got.BatteryPercent == nil || *got.BatteryPercent != 87 {
		t.Fatalf("battery percent = %v, want 87", got.BatteryPercent)
	}
	if got.PowerConnected == nil || !*got.PowerConnected {
		t.Fatalf("power connected = %v, want true", got.PowerConnected)
	}
	if got.PowerSource != "usb" || got.BatteryStatus != "charging" || got.BatteryState != BatteryStateCharging {
		t.Fatalf("snapshot = %+v, want usb charging", got)
	}
}

func TestParseBatterySnapshotUSBPoweredButCurrentDraining(t *testing.T) {
	got, err := parseBatterySnapshot("Current Battery Service state:\n  AC powered: false\n  USB powered: true\n  Wireless powered: false\n  Dock powered: false\n  status: 2\n  level: 53\n  current now: -479\n")
	if err != nil {
		t.Fatalf("parseBatterySnapshot returned error: %v", err)
	}
	if got.BatteryState != BatteryStatePluggedDraining {
		t.Fatalf("BatteryState = %q, want plugged_draining", got.BatteryState)
	}
}

func TestParseBatterySnapshotSamsungHistoryShowsPluggedDraining(t *testing.T) {
	got, err := parseBatterySnapshot(`Current Battery Service state:
  AC powered: false
  USB powered: true
  Wireless powered: false
  Dock powered: false
  status: 2
  level: 53
  current now: 115
[BattActionChangedLogBuffer]
06-27 04:18:12.894  Sending ACTION_BATTERY_CHANGED: level:69, status:2, health:2, ac:false, usb:true, wireless:false, pogo:false, current_avg:-180
06-27 12:37:24.271  Sending ACTION_BATTERY_CHANGED: level:53, status:2, health:2, ac:false, usb:true, wireless:false, pogo:false, current_avg:-183
`)
	if err != nil {
		t.Fatalf("parseBatterySnapshot returned error: %v", err)
	}
	if got.BatteryTrendPercentPerHour == nil || *got.BatteryTrendPercentPerHour >= -1.8 {
		t.Fatalf("BatteryTrendPercentPerHour = %v, want strong negative trend", got.BatteryTrendPercentPerHour)
	}
	if got.BatteryCurrentAvg == nil || *got.BatteryCurrentAvg != -183 {
		t.Fatalf("BatteryCurrentAvg = %v, want -183", got.BatteryCurrentAvg)
	}
	if got.BatteryState != BatteryStatePluggedDraining {
		t.Fatalf("BatteryState = %q, want plugged_draining", got.BatteryState)
	}
}

func TestParseBatterySnapshotUnpluggedDischarging(t *testing.T) {
	got, err := parseBatterySnapshot("Current Battery Service state:\n  AC powered: false\n  USB powered: false\n  Wireless powered: false\n  Dock powered: false\n  status: 3\n  level: 42\n")
	if err != nil {
		t.Fatalf("parseBatterySnapshot returned error: %v", err)
	}
	if got.PowerConnected == nil || *got.PowerConnected {
		t.Fatalf("power connected = %v, want false", got.PowerConnected)
	}
	if got.PowerSource != "none" || got.BatteryState != BatteryStateDischarging {
		t.Fatalf("snapshot = %+v, want unplugged draining", got)
	}
}

func TestParseBatterySnapshotFull(t *testing.T) {
	got, err := parseBatterySnapshot("Current Battery Service state:\n  AC powered: true\n  USB powered: false\n  Wireless powered: false\n  Dock powered: false\n  status: 5\n  level: 100\n")
	if err != nil {
		t.Fatalf("parseBatterySnapshot returned error: %v", err)
	}
	if got.PowerSource != "ac" || got.BatteryStatus != "full" || got.BatteryState != BatteryStateFull {
		t.Fatalf("snapshot = %+v, want ac full", got)
	}
}

func TestParseBatterySnapshotUnknownOutput(t *testing.T) {
	got, err := parseBatterySnapshot("not battery output")
	if err != nil {
		t.Fatalf("parseBatterySnapshot returned error: %v", err)
	}
	if got != (batterySnapshot{}) {
		t.Fatalf("snapshot = %+v, want empty", got)
	}
}

func TestListDevicesIncludesLocalDevices(t *testing.T) {
	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": localADBOutput,
		},
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", Platform: PlatformAndroid, State: "device", NodeID: "local-node"},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}

	expectedCalls := []string{""}
	if diff := cmp.Diff(expectedCalls, fake.calls); diff != "" {
		t.Fatalf("adb calls mismatch (-want +got):\n%s", diff)
	}
}

func TestListDevicesIncludesLocalBattery(t *testing.T) {
	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	battery := 64
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": localADBOutput,
		},
		shellOutputs: map[string][]byte{
			"local-123": []byte("Current Battery Service state:\n  USB powered: true\n  status: 2\n  level: 64\n"),
		},
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{
			Serial:   "local-123",
			Platform: PlatformAndroid,
			State:    "device",
			Battery:  &DeviceBattery{Percent: &battery, State: BatteryStateCharging},
			NodeID:   "local-node",
		},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}

	expectedShellCalls := []shellCall{
		{Host: "", Serial: "local-123", Args: []string{"dumpsys", "battery"}},
	}
	if diff := cmp.Diff(expectedShellCalls, fake.shellOutputCalls); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
	}
}

func TestListDevicesUsesCachedBatteryWhenBatteryFails(t *testing.T) {
	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	battery := 64
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": localADBOutput,
		},
		shellOutputs: map[string][]byte{
			"local-123": []byte("Current Battery Service state:\n  USB powered: true\n  status: 2\n  level: 64\n"),
		},
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	if _, err := node.ListDevices(); err != nil {
		t.Fatalf("first ListDevices returned error: %v", err)
	}
	fake.shellOutputs = nil
	fake.shellErrors = map[string]error{"local-123": errors.New("battery failed")}

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("second ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{
			Serial:   "local-123",
			Platform: PlatformAndroid,
			State:    "device",
			Battery:  &DeviceBattery{Percent: &battery, State: BatteryStateCharging},
			NodeID:   "local-node",
		},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}
}

func TestListDevicesKeepsDeviceWhenBatteryFails(t *testing.T) {
	expectedErr := errors.New("battery failed")
	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": localADBOutput,
		},
		shellErrors: map[string]error{
			"local-123": expectedErr,
		},
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", Platform: PlatformAndroid, State: "device", NodeID: "local-node"},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}
}

func TestListDevicesIncludesAndroidEnabledPeerDevices(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	remoteADBOutput := []byte("List of devices attached\nremote-456\tdevice\n")
	remoteBattery := 42
	nodeAADB := &fakeADB{
		outputs: map[string][]byte{
			"": localADBOutput,
		},
	}
	nodeBADB := &fakeADB{
		outputs: map[string][]byte{
			"": remoteADBOutput,
		},
		shellOutputs: map[string][]byte{
			"remote-456": []byte("level: 42\n"),
		},
	}
	nodeA.adb = nodeAADB
	nodeB.adb = nodeBADB
	nodeA.AndroidEnabled = true
	nodeB.AndroidEnabled = true

	connectNodePair(t, nodeA, nodeB)

	got, err := nodeA.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", Platform: PlatformAndroid, State: "device", NodeID: "a"},
		{Serial: "remote-456", Platform: PlatformAndroid, State: "device", Battery: &DeviceBattery{Percent: &remoteBattery, State: BatteryStateUnknown}, NodeID: "b"},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff([]string{""}, nodeAADB.calls); diff != "" {
		t.Fatalf("node A adb calls mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{""}, nodeBADB.calls); diff != "" {
		t.Fatalf("node B adb calls mismatch (-want +got):\n%s", diff)
	}
}

func TestListDevicesReturnsLocalDevicesWhenPeerListingFails(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	nodeA.adb = &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nlocal-123\tdevice\n"),
		},
	}
	expectedErr := errors.New("peer adb failed")
	nodeB.adb = &fakeADB{
		errors: map[string]error{
			"": expectedErr,
		},
	}
	nodeA.AndroidEnabled = true
	nodeB.AndroidEnabled = true
	connectNodePair(t, nodeA, nodeB)

	got, err := nodeA.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", Platform: PlatformAndroid, State: "device", NodeID: "a"},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}

	nodes := nodeA.ListNodes()
	index := slices.IndexFunc(nodes, func(info NodeInfo) bool {
		return info.ID == "b"
	})
	if index == -1 {
		t.Fatalf("peer node missing from ListNodes: %+v", nodes)
	}
	if !strings.Contains(nodes[index].DeviceError, expectedErr.Error()) {
		t.Fatalf("DeviceError = %q, want it to contain %q", nodes[index].DeviceError, expectedErr.Error())
	}
}

func TestDeviceBySerialReturnsHealthyPeerBeforeSlowPeer(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	nodeC, err := NewNode("c", ":0", "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = nodeC.Listen() }()
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()
	defer func() { _ = nodeC.Close() }()

	never := make(chan struct{})
	nodeB.AndroidEnabled = true
	nodeB.adb = &fakeADB{
		deviceWaits: map[string]<-chan struct{}{
			"": never,
		},
	}
	nodeC.AndroidEnabled = true
	nodeC.adb = &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}

	connectPeerToNode(t, nodeB, nodeA)
	connectPeerToNode(t, nodeC, nodeA)

	started := time.Now()
	device, err := nodeA.DeviceBySerial("remote-123")
	if err != nil {
		t.Fatalf("DeviceBySerial returned error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("DeviceBySerial took %s, want it to skip waiting for the slow peer", elapsed)
	}

	expected := &DeviceInfo{
		Serial:   "remote-123",
		Platform: PlatformAndroid,
		State:    "device",
		NodeID:   "c",
	}
	if diff := cmp.Diff(expected, device); diff != "" {
		t.Fatalf("device mismatch (-want +got):\n%s", diff)
	}
}

func TestListDevicesSkipsPeersWithoutAndroidEnabled(t *testing.T) {
	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	remoteADBOutput := []byte("List of devices attached\nremote-456\tdevice\n")
	fake := &fakeADB{
		outputs: map[string][]byte{
			"":         localADBOutput,
			"10.0.0.2": remoteADBOutput,
		},
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		Peers: map[string]*PeerConn{
			"remote-node": {
				AndroidEnabled: false,
				Addr:           "10.0.0.2",
			},
		},
		adb: fake,
	}

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", Platform: PlatformAndroid, State: "device", NodeID: "local-node"},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}

	expectedCalls := []string{""}
	if diff := cmp.Diff(expectedCalls, fake.calls); diff != "" {
		t.Fatalf("adb calls mismatch (-want +got):\n%s", diff)
	}
}

func TestListDevicesReturnsADBError(t *testing.T) {
	expectedErr := errors.New("adb failed")
	fake := &fakeADB{
		errors: map[string]error{
			"": expectedErr,
		},
	}
	node := &Node{
		ID:             "local-node",
		AdvertiseHost:  "100.64.0.1",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	_, err := node.ListDevices()
	if !errors.Is(err, expectedErr) {
		t.Fatalf("ListDevices error = %v, want %v", err, expectedErr)
	}
}

// Verifies app-level stream options are translated to scrcpy server key=value args.
func TestStreamOptionsFormat(t *testing.T) {
	opts := streamcfg.Options{
		NoAudio:      true,
		NoControl:    true,
		StayAwake:    true,
		MaxSize:      1080,
		VideoBitrate: 8000000,
	}

	got := opts.Format()
	expected := []string{
		"audio=false",
		"control=false",
		"stay_awake=true",
		"clipboard_autosync=false",
		"video_bit_rate=8000000",
		"max_size=1080",
	}

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("stream options mismatch (-want +got):\n%s", diff)
	}
}

func TestStreamOptionsFormatIncludesExplicitVideoCodecOptions(t *testing.T) {
	opts := streamcfg.Options{
		VideoCodecOptions: "i-frame-interval=1",
	}

	got := opts.Format()
	expected := []string{
		"audio=true",
		"control=true",
		"stay_awake=false",
		"clipboard_autosync=false",
		"video_codec_options=i-frame-interval=1",
	}

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("stream options mismatch (-want +got):\n%s", diff)
	}
}

// Verifies StartStream pushes scrcpy, creates a reverse tunnel, and starts the server.
func TestStartStreamSetsUpLocalDeviceStream(t *testing.T) {
	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": localADBOutput,
		},
	}
	node := &Node{
		ID:             "local-node",
		AdvertiseHost:  "100.64.0.1",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}
	opts := streamcfg.Options{
		NoAudio:      true,
		NoControl:    true,
		StayAwake:    true,
		MaxSize:      1080,
		VideoBitrate: 8000000,
	}

	session, err := node.StartStream("local-123", opts)
	if err != nil {
		t.Fatalf("StartStream returned error: %v", err)
	}
	defer func() { _ = session.Stop() }()

	if session.DeviceSerial != "local-123" {
		t.Fatalf("DeviceSerial = %q, want %q", session.DeviceSerial, "local-123")
	}
	if session.LocalPort == 0 {
		t.Fatal("LocalPort = 0, want an assigned port")
	}
	if session.Host != "100.64.0.1" {
		t.Fatalf("Host = %q, want %q", session.Host, "100.64.0.1")
	}

	if len(fake.pushCalls) != 1 {
		t.Fatalf("push call count = %d, want 1", len(fake.pushCalls))
	}
	if fake.pushCalls[0].Host != "" {
		t.Fatalf("push host = %q, want local host", fake.pushCalls[0].Host)
	}
	if fake.pushCalls[0].Serial != "local-123" {
		t.Fatalf("push serial = %q, want %q", fake.pushCalls[0].Serial, "local-123")
	}
	if fake.pushCalls[0].LocalPath == "" {
		t.Fatal("push local path is empty")
	}
	if fake.pushCalls[0].RemotePath != scrcpy.RemotePath {
		t.Fatalf("push remote path = %q, want %q", fake.pushCalls[0].RemotePath, scrcpy.RemotePath)
	}

	expectedReverseCalls := []reverseCall{
		{Host: "", Serial: "local-123", DeviceSocket: scrcpy.DeviceSocket, LocalPort: session.LocalPort},
	}
	if diff := cmp.Diff(expectedReverseCalls, fake.reverseCalls); diff != "" {
		t.Fatalf("reverse calls mismatch (-want +got):\n%s", diff)
	}

	expectedShellCalls := []shellCall{
		{
			Host:   "",
			Serial: "local-123",
			Args: []string{
				"CLASSPATH=" + scrcpy.RemotePath,
				"app_process",
				"/",
				"com.genymobile.scrcpy.Server",
				scrcpy.ServerVersion,
				"audio=false",
				"control=false",
				"stay_awake=true",
				"clipboard_autosync=false",
				"video_bit_rate=8000000",
				"max_size=1080",
			},
		},
	}
	if diff := cmp.Diff(expectedShellCalls, fake.shellCalls); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
	}
}

func TestStartStreamTurnsScreenOffAfterControlConnects(t *testing.T) {
	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	controlMessages := make(chan []byte, 1)
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": localADBOutput,
		},
		controlMessages: controlMessages,
	}
	node := &Node{
		ID:             "local-node",
		AdvertiseHost:  "100.64.0.1",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
	}

	session, err := node.StartStream("local-123", streamcfg.Options{
		NoAudio:       true,
		TurnScreenOff: true,
	})
	if err != nil {
		t.Fatalf("StartStream returned error: %v", err)
	}
	defer func() { _ = session.Stop() }()

	select {
	case got := <-controlMessages:
		if want := []byte{scrcpy.SetDisplayPower, 0}; !cmp.Equal(got, want) {
			t.Fatalf("control message mismatch (-want +got):\n%s", cmp.Diff(want, got))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for display power control message")
	}
}

func TestStartStreamRejectsTurnScreenOffWithoutControl(t *testing.T) {
	node := &Node{}

	_, err := node.startLocalAndroidStream("local-123", streamcfg.Options{
		NoControl:     true,
		TurnScreenOff: true,
	})
	if err == nil || !strings.Contains(err.Error(), "turn_screen_off requires control") {
		t.Fatalf("startLocalAndroidStream error = %v, want turn_screen_off requires control", err)
	}
}

func TestScreenshotLocalUsesExecOut(t *testing.T) {
	node := newControlTestNode("node-a", "local-123")
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nlocal-123\tdevice\n"),
		},
		execOutOutputs: map[string][]byte{
			"local-123": []byte("png-bytes"),
		},
	}
	node.adb = fake

	got, err := node.Screenshot("local-123")
	if err != nil {
		t.Fatalf("Screenshot returned error: %v", err)
	}
	if string(got) != "png-bytes" {
		t.Fatalf("screenshot = %q, want png-bytes", got)
	}
	if len(fake.shellOutputCalls) == 0 {
		t.Fatal("ExecOut was not called")
	}
	call := fake.shellOutputCalls[len(fake.shellOutputCalls)-1]
	if call.Serial != "local-123" || strings.Join(call.Args, " ") != "exec-out screencap -p" {
		t.Fatalf("exec-out call = %+v", call)
	}
}

func TestScreenshotRemoteRoutesToPeerOwner(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	nodeA.adb = &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\n"),
		},
	}
	nodeB.adb = &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
		shellOutputs: map[string][]byte{
			"remote-123": []byte("level: 80"),
		},
		execOutOutputs: map[string][]byte{
			"remote-123": []byte("peer-png"),
		},
	}
	nodeB.AndroidEnabled = true
	connectNodePair(t, nodeA, nodeB)

	got, err := nodeA.Screenshot("remote-123")
	if err != nil {
		t.Fatalf("Screenshot returned error: %v", err)
	}
	if string(got) != "peer-png" {
		t.Fatalf("screenshot = %q, want peer-png", got)
	}
	fake := nodeB.adb.(*fakeADB)
	if len(fake.shellOutputCalls) == 0 {
		t.Fatal("peer ExecOut was not called")
	}
	call := fake.shellOutputCalls[len(fake.shellOutputCalls)-1]
	if call.Serial != "remote-123" || strings.Join(call.Args, " ") != "exec-out screencap -p" {
		t.Fatalf("peer exec-out call = %+v", call)
	}
}
