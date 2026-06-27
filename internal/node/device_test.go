package node

import (
	"encoding/binary"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/brijorn/mast/internal/scrcpy"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/google/go-cmp/cmp"
)

type pushCall struct {
	Host       string
	LocalPath  string
	RemotePath string
}

type reverseCall struct {
	Host         string
	DeviceSocket string
	LocalPort    int
}

type shellCall struct {
	Host   string
	Serial string
	Args   []string
}

type fakeADB struct {
	outputs          map[string][]byte
	errors           map[string]error
	shellOutputs     map[string][]byte
	shellErrors      map[string]error
	calls            []string
	pushCalls        []pushCall
	reverseCalls     []reverseCall
	shellCalls       []shellCall
	shellOutputCalls []shellCall
}

func (a *fakeADB) Devices(host string) ([]byte, error) {
	a.calls = append(a.calls, host)
	if err := a.errors[host]; err != nil {
		return nil, err
	}
	return a.outputs[host], nil
}

func (a *fakeADB) Push(host string, localPath string, remotePath string) error {
	a.pushCalls = append(a.pushCalls, pushCall{
		Host:       host,
		LocalPath:  localPath,
		RemotePath: remotePath,
	})
	return nil
}

func (a *fakeADB) Reverse(host string, deviceSocket string, localPort int) error {
	a.reverseCalls = append(a.reverseCalls, reverseCall{
		Host:         host,
		DeviceSocket: deviceSocket,
		LocalPort:    localPort,
	})
	return nil
}

func (a *fakeADB) StartShell(host string, arg ...string) (*exec.Cmd, error) {
	a.shellCalls = append(a.shellCalls, shellCall{
		Host: host,
		Args: append([]string(nil), arg...),
	})
	if len(a.reverseCalls) > 0 {
		port := a.reverseCalls[len(a.reverseCalls)-1].LocalPort
		go writeFakeScrcpyVideoMetadata(port)
	}
	return nil, nil
}

func (a *fakeADB) Shell(host string, serial string, arg ...string) ([]byte, error) {
	a.shellOutputCalls = append(a.shellOutputCalls, shellCall{
		Host:   host,
		Serial: serial,
		Args:   append([]string(nil), arg...),
	})
	if err := a.shellErrors[serial]; err != nil {
		return nil, err
	}
	return a.shellOutputs[serial], nil
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

func TestParseDevicesOutput(t *testing.T) {
	parserADBOutput := "List of devices attached\nabc123\tdevice\nxyz789\toffline\n"
	got := parseDevicesOutput(parserADBOutput, "node-a", []DeviceInfo{})

	expected := []DeviceInfo{
		{Serial: "abc123", State: "device", NodeID: "node-a"},
		{Serial: "xyz789", State: "offline", NodeID: "node-a"},
	}

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
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

func TestListDevicesIncludesLocalDevices(t *testing.T) {
	localADBOutput := []byte("List of devices attached\nlocal-123\tdevice\n")
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": localADBOutput,
		},
	}
	node := &Node{
		ID:    "local-node",
		Peers: map[string]*PeerConn{},
		adb:   fake,
	}

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", State: "device", NodeID: "local-node"},
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
			"local-123": []byte("Current Battery Service state:\n  level: 64\n"),
		},
	}
	node := &Node{
		ID:    "local-node",
		Peers: map[string]*PeerConn{},
		adb:   fake,
	}

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", State: "device", BatteryPercent: &battery, NodeID: "local-node"},
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
		ID:    "local-node",
		Peers: map[string]*PeerConn{},
		adb:   fake,
	}

	got, err := node.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", State: "device", NodeID: "local-node"},
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
	nodeB.AndroidEnabled = true

	connectNodePair(t, nodeA, nodeB)

	got, err := nodeA.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	expected := []DeviceInfo{
		{Serial: "local-123", State: "device", NodeID: "a"},
		{Serial: "remote-456", State: "device", BatteryPercent: &remoteBattery, NodeID: "b"},
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
		ID: "local-node",
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
		{Serial: "local-123", State: "device", NodeID: "local-node"},
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
		ID:            "local-node",
		AdvertiseHost: "100.64.0.1",
		Peers:         map[string]*PeerConn{},
		adb:           fake,
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
		"video_codec_options=i-frame-interval=2",
		"video_bit_rate=8000000",
		"max_size=1080",
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
		ID:            "local-node",
		AdvertiseHost: "100.64.0.1",
		Peers:         map[string]*PeerConn{},
		adb:           fake,
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
	if fake.pushCalls[0].LocalPath == "" {
		t.Fatal("push local path is empty")
	}
	if fake.pushCalls[0].RemotePath != scrcpy.RemotePath {
		t.Fatalf("push remote path = %q, want %q", fake.pushCalls[0].RemotePath, scrcpy.RemotePath)
	}

	expectedReverseCalls := []reverseCall{
		{Host: "", DeviceSocket: scrcpy.DeviceSocket, LocalPort: session.LocalPort},
	}
	if diff := cmp.Diff(expectedReverseCalls, fake.reverseCalls); diff != "" {
		t.Fatalf("reverse calls mismatch (-want +got):\n%s", diff)
	}

	expectedShellCalls := []shellCall{
		{
			Host: "",
			Args: []string{
				"CLASSPATH=" + scrcpy.RemotePath,
				"app_process",
				"/",
				"com.genymobile.scrcpy.Server",
				scrcpy.ServerVersion,
				"audio=false",
				"control=false",
				"stay_awake=true",
				"video_codec_options=i-frame-interval=2",
				"video_bit_rate=8000000",
				"max_size=1080",
			},
		},
	}
	if diff := cmp.Diff(expectedShellCalls, fake.shellCalls); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
	}
}
