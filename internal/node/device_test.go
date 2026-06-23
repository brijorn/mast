package node

import (
	"errors"
	"os/exec"
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
	Host string
	Args []string
}

type fakeADB struct {
	outputs      map[string][]byte
	errors       map[string]error
	calls        []string
	pushCalls    []pushCall
	reverseCalls []reverseCall
	shellCalls   []shellCall
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
	return nil, nil
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

func TestListDevicesIncludesAndroidEnabledPeerDevices(t *testing.T) {
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
				AndroidEnabled: true,
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
		{Serial: "remote-456", State: "device", NodeID: "remote-node"},
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("devices mismatch (-want +got):\n%s", diff)
	}

	expectedCalls := []string{"", "10.0.0.2"}
	if diff := cmp.Diff(expectedCalls, fake.calls); diff != "" {
		t.Fatalf("adb calls mismatch (-want +got):\n%s", diff)
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
				"video_bit_rate=8000000",
				"max_size=1080",
			},
		},
	}
	if diff := cmp.Diff(expectedShellCalls, fake.shellCalls); diff != "" {
		t.Fatalf("shell calls mismatch (-want +got):\n%s", diff)
	}
}
