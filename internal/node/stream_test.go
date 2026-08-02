package node

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/scrcpy"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/google/go-cmp/cmp"
)

func TestStreamSessionStopIgnoresKilledProcessExit(t *testing.T) {
	if os.Getenv("MAST_STOP_HELPER_PROCESS") == "1" {
		select {}
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestStreamSessionStopIgnoresKilledProcessExit")
	cmd.Env = append(os.Environ(), "MAST_STOP_HELPER_PROCESS=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	session := &StreamSession{cmd: cmd}
	if err := session.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
}

func TestReadScrcpyVideoMetadata(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	go func() {
		deviceName := make([]byte, 64)
		copy(deviceName, "A15")

		streamMeta := make([]byte, 16)
		copy(streamMeta[:4], "h264")
		binary.BigEndian.PutUint32(streamMeta[4:8], 0x80000000)
		binary.BigEndian.PutUint32(streamMeta[8:12], 944)
		binary.BigEndian.PutUint32(streamMeta[12:16], 1080)

		_, _ = client.Write(deviceName)
		_, _ = client.Write(streamMeta)
	}()

	name, width, height, err := readScrcpyVideoMetadata(server)
	if err != nil {
		t.Fatalf("readScrcpyVideoMetadata returned error: %v", err)
	}

	if name != "A15" {
		t.Fatalf("name = %q, want %q", name, "A15")
	}
	if width != 944 {
		t.Fatalf("width = %d, want %d", width, 944)
	}
	if height != 1080 {
		t.Fatalf("height = %d, want %d", height, 1080)
	}
}

func TestViewerStreamKeepsStaticAndroidSession(t *testing.T) {
	done := make(chan struct{})
	close(done)
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcastAt(
		VideoPacket{PTS: 1, Keyframe: true, Data: annexBNAL(5, 1)},
		time.Now().Add(-time.Hour),
	)
	session := &StreamSession{
		DeviceSerial:     "local-123",
		Platform:         PlatformAndroid,
		Kind:             "h264",
		videoBroadcaster: broadcaster,
	}
	node := &Node{
		streams: map[string]*streamEntry{
			"local-123": {Session: session, Done: done},
		},
	}

	got, err := node.viewerStream("local-123")
	if err != nil {
		t.Fatalf("viewerStream returned error: %v", err)
	}
	if got != session {
		t.Fatalf("viewerStream returned %p, want static session %p", got, session)
	}
}

func TestStartStreamRoutesRemoteDeviceToPeer(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	nodeA.AndroidEnabled = true
	nodeB.AndroidEnabled = true
	nodeB.AdvertiseHost = "10.0.0.2"

	nodeAADB := &fakeADB{
		outputs: map[string][]byte{
			"":          []byte("List of devices attached\n"),
			"127.0.0.1": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}
	nodeBADB := &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}
	nodeA.adb = nodeAADB
	nodeB.adb = nodeBADB

	connectNodePair(t, nodeA, nodeB)

	session, err := nodeA.StartStream("remote-123", streamcfg.Options{
		NoAudio:   true,
		NoControl: true,
	})
	if err != nil {
		t.Fatalf("StartStream returned error: %v", err)
	}

	if session.DeviceSerial != "remote-123" {
		t.Fatalf("DeviceSerial = %q, want remote-123", session.DeviceSerial)
	}
	if session.Host != "10.0.0.2" {
		t.Fatalf("Host = %q, want 10.0.0.2", session.Host)
	}
	if session.LocalPort == 0 {
		t.Fatal("LocalPort = 0, want assigned peer port")
	}
	if len(nodeAADB.reverseCalls) != 0 {
		t.Fatalf("node A reverse calls = %d, want 0", len(nodeAADB.reverseCalls))
	}
	if diff := cmp.Diff([]string{""}, nodeAADB.calls); diff != "" {
		t.Fatalf("node A adb calls mismatch (-want +got):\n%s", diff)
	}
	if len(nodeBADB.reverseCalls) != 1 {
		t.Fatalf("node B reverse calls = %d, want 1", len(nodeBADB.reverseCalls))
	}
	if nodeBADB.reverseCalls[0].Host != "" {
		t.Fatalf("node B reverse host = %q, want local host", nodeBADB.reverseCalls[0].Host)
	}
}

func TestPeerListDevicesNotBlockedBySlowStartStream(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	nodeB.AndroidEnabled = true
	nodeB.adb = &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}
	blockedStart := &streamEntry{Done: make(chan struct{})}
	nodeB.streams["remote-123"] = blockedStart

	connectNodePair(t, nodeA, nodeB)

	startDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := nodeA.startPeerStream(ctx, "b", "remote-123", streamcfg.Options{
			NoAudio:   true,
			NoControl: true,
		})
		startDone <- err
	}()

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	devices, err := nodeA.listPeerDevices(ctx, "b")
	if err != nil {
		t.Fatalf("listPeerDevices returned error while start stream was blocked: %v", err)
	}
	if len(devices) != 1 || devices[0].Serial != "remote-123" {
		t.Fatalf("devices = %+v, want remote-123", devices)
	}

	blockedStart.Session = &StreamSession{
		ID:           "blocked-stream",
		DeviceSerial: "remote-123",
		Platform:     PlatformAndroid,
		Kind:         "h264",
	}
	close(blockedStart.Done)

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("startPeerStream returned error after unblock: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("startPeerStream did not finish after unblock")
	}
}

func TestPeerMJPEGURLUsesPeerAPIAddr(t *testing.T) {
	node := &Node{
		ID: "local-node",
		Peers: map[string]*PeerConn{
			"mac-node": {
				Addr:    "100.103.16.24",
				APIAddr: ":7001",
			},
		},
	}

	got, err := node.peerMJPEGURL("mac-node", "ios-123")
	if err != nil {
		t.Fatalf("peerMJPEGURL returned error: %v", err)
	}

	want := "http://100.103.16.24:7001/api/streams/mjpeg?serial=ios-123"
	if got != want {
		t.Fatalf("peerMJPEGURL = %q, want %q", got, want)
	}
}

func TestPeerMJPEGURLDefaultsPeerAPIAddr(t *testing.T) {
	node := &Node{
		ID: "local-node",
		Peers: map[string]*PeerConn{
			"mac-node": {
				Addr: "100.103.16.24",
			},
		},
	}

	got, err := node.peerMJPEGURL("mac-node", "ios-123")
	if err != nil {
		t.Fatalf("peerMJPEGURL returned error: %v", err)
	}

	want := "http://100.103.16.24:6271/api/streams/mjpeg?serial=ios-123"
	if got != want {
		t.Fatalf("peerMJPEGURL = %q, want %q", got, want)
	}
}

func TestPeerVideoURLUsesPeerAPIAddr(t *testing.T) {
	node := &Node{
		ID: "local-node",
		Peers: map[string]*PeerConn{
			"android-node": {
				Addr:    "100.99.89.88",
				APIAddr: ":7001",
			},
		},
	}

	got, err := node.peerVideoURL("android-node", "android-123")
	if err != nil {
		t.Fatalf("peerVideoURL returned error: %v", err)
	}

	want := "ws://100.99.89.88:7001/api/streams/video?serial=android-123"
	if got != want {
		t.Fatalf("peerVideoURL = %q, want %q", got, want)
	}
}

func TestVideoViewerURLAddsRequiredViewer(t *testing.T) {
	got, err := videoViewerURL("ws://100.99.89.88:7001/api/streams/video?serial=android-123", "peer-viewer")
	if err != nil {
		t.Fatal(err)
	}
	want := "ws://100.99.89.88:7001/api/streams/video?serial=android-123&viewer=peer-viewer"
	if got != want {
		t.Fatalf("videoViewerURL = %q, want %q", got, want)
	}
}

func TestEnsureStreamLocalUsesLightweightDeviceLookup(t *testing.T) {
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nlocal-123\tdevice\n"),
		},
	}
	node := &Node{
		ID:             "local-node",
		AdvertiseHost:  "127.0.0.1",
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            fake,
		streams:        make(map[string]*streamEntry),
	}

	session, err := node.EnsureStream("local-123", streamcfg.Options{
		NoAudio:   true,
		NoControl: true,
	})
	if err != nil {
		t.Fatalf("EnsureStream returned error: %v", err)
	}
	t.Cleanup(func() { _ = session.Stop() })

	if diff := cmp.Diff([]string{""}, fake.calls); diff != "" {
		t.Fatalf("adb devices calls mismatch (-want +got):\n%s", diff)
	}
	for _, call := range fake.shellOutputCalls {
		if len(call.Args) >= 2 && call.Args[0] == "dumpsys" && call.Args[1] == "battery" {
			t.Fatalf("EnsureStream called battery enrichment: %+v", call)
		}
	}
}

func TestEnsureStreamRestartsRemoteStreamAfterPeerLosesState(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	nodeA.AndroidEnabled = true
	nodeB.AndroidEnabled = true
	nodeB.AdvertiseHost = "10.0.0.2"

	nodeA.adb = &fakeADB{
		outputs: map[string][]byte{
			"":          []byte("List of devices attached\n"),
			"127.0.0.1": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}
	nodeBADB := &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}
	nodeB.adb = nodeBADB

	connectNodePair(t, nodeA, nodeB)

	first, err := nodeA.EnsureStream("remote-123", streamcfg.Options{
		NoAudio:   true,
		NoControl: true,
	})
	if err != nil {
		t.Fatalf("first EnsureStream returned error: %v", err)
	}
	if len(nodeBADB.reverseCalls) != 1 {
		t.Fatalf("node B reverse calls after first stream = %d, want 1", len(nodeBADB.reverseCalls))
	}

	nodeB.streamsMu.Lock()
	for serial, entry := range nodeB.streams {
		delete(nodeB.streams, serial)
		if entry.Session != nil {
			_ = entry.Session.Stop()
		}
	}
	nodeB.streamsMu.Unlock()

	second, err := nodeA.EnsureStream("remote-123", streamcfg.Options{
		NoAudio:   true,
		NoControl: true,
	})
	if err != nil {
		t.Fatalf("second EnsureStream returned error: %v", err)
	}
	if len(nodeBADB.reverseCalls) != 2 {
		t.Fatalf("node B reverse calls after peer state reset = %d, want 2", len(nodeBADB.reverseCalls))
	}
	if second.ID == first.ID {
		t.Fatalf("second stream ID = first stream ID %q, want fresh peer stream", second.ID)
	}
}

func TestStopStreamRoutesRemoteDeviceToPeer(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	nodeA.AndroidEnabled = true
	nodeB.AndroidEnabled = true
	nodeA.adb = &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\n"),
		},
	}
	nodeB.adb = &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}
	nodeB.streams["remote-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "remote-123",
	})

	connectNodePair(t, nodeA, nodeB)

	if err := nodeA.StopStream("remote-123"); err != nil {
		t.Fatalf("StopStream returned error: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		nodeB.streamsMu.RLock()
		defer nodeB.streamsMu.RUnlock()
		_, ok := nodeB.streams["remote-123"]
		return !ok
	})
}

func TestStopLocalIOSStreamReturnsBeforeCleanupFinishes(t *testing.T) {
	done := make(chan struct{})
	close(done)
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})

	node := &Node{
		streams: map[string]*streamEntry{
			"ios-123": {
				Session: &StreamSession{
					DeviceSerial: "ios-123",
					Platform:     PlatformIOS,
					iosCleanup: func() {
						close(cleanupStarted)
						<-releaseCleanup
					},
				},
				Done: done,
			},
		},
	}
	defer close(releaseCleanup)

	start := time.Now()
	if err := node.stopLocalStream("ios-123"); err != nil {
		t.Fatalf("stopLocalStream returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("stopLocalStream took %s, want non-blocking iOS cleanup", elapsed)
	}

	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("iOS cleanup did not start")
	}

	node.streamsMu.RLock()
	_, ok := node.streams["ios-123"]
	node.streamsMu.RUnlock()
	if ok {
		t.Fatal("stream entry still present after stop")
	}
}

func TestDropStreamRemovesCurrentSession(t *testing.T) {
	done := make(chan struct{})
	close(done)
	releaseCleanup := make(chan struct{})
	session := &StreamSession{
		DeviceSerial: "ios-123",
		Platform:     PlatformIOS,
		iosCleanup: func() {
			<-releaseCleanup
		},
	}
	node := &Node{
		streams: map[string]*streamEntry{
			"ios-123": {
				Session: session,
				Done:    done,
			},
		},
	}
	defer close(releaseCleanup)

	node.DropStream("ios-123", session)

	node.streamsMu.RLock()
	_, ok := node.streams["ios-123"]
	node.streamsMu.RUnlock()
	if ok {
		t.Fatal("stream entry still present after drop")
	}
}

func TestHandleMJPEGStreamErrorKeepsSessionOnContextCancellation(t *testing.T) {
	serial := "ios-123"
	session := &StreamSession{DeviceSerial: serial, Platform: PlatformIOS, Kind: "mjpeg"}
	node := nodeWithReadyStream(serial, session)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := node.handleMJPEGStreamError(ctx, serial, session, ctx.Err()); err != nil {
		t.Fatalf("handleMJPEGStreamError returned error: %v", err)
	}

	assertStreamPresent(t, node, serial, true)
}

func TestHandleMJPEGStreamErrorKeepsSessionOnViewerDisconnect(t *testing.T) {
	serial := "ios-123"
	session := &StreamSession{DeviceSerial: serial, Platform: PlatformIOS, Kind: "mjpeg"}
	node := nodeWithReadyStream(serial, session)
	err := errors.New("write tcp 127.0.0.1:6271->127.0.0.1:52000: write: broken pipe")

	if gotErr := node.handleMJPEGStreamError(context.Background(), serial, session, err); gotErr != nil {
		t.Fatalf("handleMJPEGStreamError returned error: %v", gotErr)
	}

	assertStreamPresent(t, node, serial, true)
}

func TestHandleMJPEGStreamErrorKeepsSessionOnStreamClosed(t *testing.T) {
	serial := "ios-123"
	session := &StreamSession{DeviceSerial: serial, Platform: PlatformIOS, Kind: "mjpeg"}
	node := nodeWithReadyStream(serial, session)
	err := errors.New("stream closed")

	if gotErr := node.handleMJPEGStreamError(context.Background(), serial, session, err); gotErr != nil {
		t.Fatalf("handleMJPEGStreamError returned error: %v", gotErr)
	}

	assertStreamPresent(t, node, serial, true)
}

func TestHandleMJPEGStreamErrorDropsSessionOnStreamFailure(t *testing.T) {
	serial := "ios-123"
	session := &StreamSession{DeviceSerial: serial, Platform: PlatformIOS, Kind: "mjpeg"}
	node := nodeWithReadyStream(serial, session)
	streamErr := errors.New("mjpeg upstream failed")

	if err := node.handleMJPEGStreamError(context.Background(), serial, session, streamErr); !errors.Is(err, streamErr) {
		t.Fatalf("handleMJPEGStreamError returned %v, want %v", err, streamErr)
	}

	assertStreamPresent(t, node, serial, false)
}

func nodeWithReadyStream(serial string, session *StreamSession) *Node {
	done := make(chan struct{})
	close(done)
	return &Node{
		streams: map[string]*streamEntry{
			serial: {
				Session: session,
				Done:    done,
			},
		},
	}
}

func assertStreamPresent(t *testing.T, node *Node, serial string, want bool) {
	t.Helper()

	node.streamsMu.RLock()
	_, ok := node.streams[serial]
	node.streamsMu.RUnlock()
	if ok != want {
		t.Fatalf("stream present = %t, want %t", ok, want)
	}
}

func TestEnsureStreamReusesExistingStreamWithCachedGOP(t *testing.T) {
	node := &Node{
		streams: make(map[string]*streamEntry),
	}
	done := make(chan struct{})
	close(done)

	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Keyframe: true, Data: []byte{1}})

	existing := &StreamSession{
		ID:               "existing-stream",
		DeviceSerial:     "local-123",
		videoBroadcaster: broadcaster,
	}
	node.streams["local-123"] = &streamEntry{
		Session: existing,
		Done:    done,
	}

	startCalls := 0
	got, err := node.ensureStream("local-123", streamcfg.Options{}, func(string, streamcfg.Options) (*StreamSession, error) {
		startCalls++
		return &StreamSession{ID: "new-stream"}, nil
	})
	if err != nil {
		t.Fatalf("ensureStream returned error: %v", err)
	}
	if got != existing {
		t.Fatalf("ensureStream returned %p, want existing %p", got, existing)
	}
	if startCalls != 0 {
		t.Fatalf("start calls = %d, want 0", startCalls)
	}
}

func TestEnsureStreamKeepsStaticAndroidVideo(t *testing.T) {
	node := &Node{streams: make(map[string]*streamEntry)}
	done := make(chan struct{})
	close(done)
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcastAt(
		VideoPacket{PTS: 1, Keyframe: true, Data: annexBNAL(5, 1)},
		time.Now().Add(-time.Hour),
	)

	existing := &StreamSession{
		ID:               "static-stream",
		DeviceSerial:     "local-123",
		Platform:         PlatformAndroid,
		Kind:             "h264",
		videoBroadcaster: broadcaster,
	}
	node.streams["local-123"] = &streamEntry{Session: existing, Done: done}

	startCalls := 0
	got, err := node.ensureStream("local-123", streamcfg.Options{NoControl: true}, func(string, streamcfg.Options) (*StreamSession, error) {
		startCalls++
		return &StreamSession{ID: "fresh-stream"}, nil
	})
	if err != nil {
		t.Fatalf("ensureStream returned error: %v", err)
	}
	if got != existing {
		t.Fatalf("ensureStream returned %p, want static stream %p", got, existing)
	}
	if startCalls != 0 {
		t.Fatalf("start calls = %d, want 0", startCalls)
	}
}

func TestWaitForInitialVideoRunsRecoveryBeforeRetry(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	packets, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()
	recoveryCalls := 0
	recoverStartup := func() {
		recoveryCalls++
		broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)})
		broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)})
	}

	if err := waitForInitialVideo(context.Background(), packets, recoverStartup, time.Nanosecond, time.Second); err != nil {
		t.Fatalf("waitForInitialVideo returned error: %v", err)
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery calls = %d, want 1", recoveryCalls)
	}
}

func TestAndroidDeviceInteractiveParsesPowerManagerState(t *testing.T) {
	tests := []struct {
		name string
		dump string
		want bool
	}{
		{name: "explicit interactive", dump: "  mInteractive=true\n  mWakefulness=Asleep\n", want: true},
		{name: "awake", dump: "  mWakefulness=Awake\n", want: true},
		{name: "dreaming", dump: "Wakefulness: Dreaming\n", want: true},
		{name: "asleep", dump: "  mWakefulness=Asleep\n", want: false},
		{name: "dozing", dump: "Wakefulness: Dozing\n", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := androidDeviceInteractive([]byte(test.dump))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("interactive = %v, want %v", got, test.want)
			}
		})
	}
}

func TestVideoStartupDoesNotWakeInteractiveAndroidDevice(t *testing.T) {
	fake := &fakeADB{shellCommandOutputs: map[string][]byte{
		shellCommandKey("local-123", "dumpsys", "power"): []byte("mWakefulness=Awake\n"),
	}}
	control := &recordingConn{}
	n := &Node{
		ctx:         context.Background(),
		adb:         fake,
		configReady: true,
		configState: mastconfig.Config{AndroidEnabled: true},
	}

	n.recoverAndroidVideoStartup("", "local-123", &StreamSession{controlConn: control})

	if got := control.dataSnapshot(); len(got) != 0 {
		t.Fatalf("interactive device received display-power message %v", got)
	}
	for _, call := range fake.shellOutputCallsSnapshot() {
		if cmp.Equal(call.Args, []string{"input", "keyevent", "KEYCODE_WAKEUP"}) {
			t.Fatalf("interactive device received wake keyevent: %+v", call)
		}
	}
}

func TestVideoStartupWakesConfirmedNonInteractiveDevice(t *testing.T) {
	fake := &fakeADB{shellCommandOutputs: map[string][]byte{
		shellCommandKey("local-123", "dumpsys", "power"): []byte("mInteractive=false\n"),
	}}
	control := &recordingConn{}
	n := &Node{
		ctx:         context.Background(),
		adb:         fake,
		configReady: true,
		configState: mastconfig.Config{AndroidEnabled: true},
	}

	n.recoverAndroidVideoStartup("", "local-123", &StreamSession{controlConn: control})

	want := []byte{scrcpy.SetDisplayPower, 1}
	if got := control.dataSnapshot(); !cmp.Equal(got, want) {
		t.Fatalf("display-power message mismatch (-want +got):\n%s", cmp.Diff(want, got))
	}
}

// A dark panel maintained by the display-off policy is the likeliest reason
// startup saw no keyframe — some devices stop composing entirely while the
// panel is off, and such a device can report Awake with a frozen pipeline
// (observed on an SM_S156V, 2026-08-02: policy preservation looped forever
// while a wake key event recovered video immediately). So under the policy the
// recovery must wake the display through both channels rather than re-assert
// the very state that blocks the keyframe.
func TestVideoStartupWakesDisplayDespiteDisplayOffPolicy(t *testing.T) {
	fake := &fakeADB{shellCommandOutputs: map[string][]byte{
		shellCommandKey("local-123", "dumpsys", "power"): []byte("mWakefulness=Awake\n"),
	}}
	viewerControl := &recordingConn{}
	policyControl := &recordingConn{}
	n := &Node{
		ctx:                 context.Background(),
		adb:                 fake,
		configReady:         true,
		configState:         mastconfig.Config{AndroidEnabled: true, KeepDisplayOff: true},
		devicePowerReady:    map[string]bool{"local-123": true},
		devicePowerSessions: map[string]*devicePowerSession{"local-123": {serial: "local-123", control: policyControl}},
	}

	n.recoverAndroidVideoStartup("", "local-123", &StreamSession{controlConn: viewerControl})

	want := []byte{scrcpy.SetDisplayPower, 1}
	if got := viewerControl.dataSnapshot(); !cmp.Equal(got, want) {
		t.Fatalf("viewer wake message mismatch (-want +got):\n%s", cmp.Diff(want, got))
	}
	wokeDevice := false
	for _, call := range fake.shellOutputCallsSnapshot() {
		if cmp.Equal(call.Args, []string{"input", "keyevent", "KEYCODE_WAKEUP"}) {
			wokeDevice = true
		}
	}
	if !wokeDevice {
		t.Fatal("device did not receive the wake keyevent that unfreezes a wedged compositor")
	}
	if got := policyControl.dataSnapshot(); len(got) != 0 {
		t.Fatalf("policy session re-asserted display power during startup recovery: %v", got)
	}
}

// scrcpy encodes on display damage, so a static screen is legitimately
// silent; quiet earns a device probe, never a verdict on its own.
func TestSuspectAndroidRequiresQuietBeyondWindow(t *testing.T) {
	b := newVideoBroadcaster()
	s := &StreamSession{Platform: PlatformAndroid, videoBroadcaster: b}

	now := time.Now()
	if s.isSuspectAndroid(now) {
		t.Fatal("session with no packets yet reported suspect")
	}
	b.broadcastAt(VideoPacket{Data: []byte{1}, Keyframe: true}, now)
	if s.isSuspectAndroid(now.Add(androidLivenessQuietWindow / 2)) {
		t.Fatal("recently active session reported suspect")
	}
	if !s.isSuspectAndroid(now.Add(androidLivenessQuietWindow + time.Second)) {
		t.Fatal("long-silent session not reported suspect")
	}
}

// The device-side scrcpy server can die while every host-side signal stays
// healthy — the adb client survives and the sockets stay open (observed
// 2026-08-02: a 17-hour-old adb client with no app_process on the device,
// serving "waiting for video" forever). Only a device probe tells a zombie
// from a healthy quiet session, and the power-policy session's scid argument
// must not count as the viewer server.
func TestAndroidServerAliveDistinguishesViewerFromPolicySession(t *testing.T) {
	deadOutput := []byte("ARGS\napp_process / com.genymobile.scrcpy.Server 4.0 scid=4d415354 video=false\n")
	liveOutput := []byte(string(deadOutput) + "app_process / com.genymobile.scrcpy.Server 4.0 audio=false control=true\n")

	fake := &fakeADB{shellCommandOutputs: map[string][]byte{
		shellCommandKey("local-123", "ps", "-A", "-o", "ARGS"): deadOutput,
	}}
	n := &Node{ctx: context.Background(), adb: fake}
	if n.androidServerAlive("", "local-123") {
		t.Fatal("policy-only process list reported the viewer server alive")
	}

	fake = &fakeADB{shellCommandOutputs: map[string][]byte{
		shellCommandKey("local-123", "ps", "-A", "-o", "ARGS"): liveOutput,
	}}
	n = &Node{ctx: context.Background(), adb: fake}
	if !n.androidServerAlive("", "local-123") {
		t.Fatal("live viewer server reported dead")
	}
}

// ensureStream must hand a new viewer a working session: a long-quiet session
// whose device-side server is gone gets rebuilt, and one whose server is
// alive is kept even after an hour of static screen.
func TestEnsureStreamRebuildsZombieAndroidSession(t *testing.T) {
	deadOutput := []byte("ARGS\napp_process / com.genymobile.scrcpy.Server 4.0 scid=4d415354 video=false\n")
	fake := &fakeADB{shellCommandOutputs: map[string][]byte{
		shellCommandKey("local-123", "ps", "-A", "-o", "ARGS"): deadOutput,
	}}
	node := &Node{ctx: context.Background(), adb: fake, streams: make(map[string]*streamEntry)}
	done := make(chan struct{})
	close(done)
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcastAt(
		VideoPacket{PTS: 1, Keyframe: true, Data: annexBNAL(5, 1)},
		time.Now().Add(-time.Hour),
	)
	existing := &StreamSession{
		ID:               "zombie-stream",
		DeviceSerial:     "local-123",
		Platform:         PlatformAndroid,
		Kind:             "h264",
		videoBroadcaster: broadcaster,
	}
	node.streams["local-123"] = &streamEntry{Session: existing, Done: done}

	fresh := &StreamSession{ID: "fresh-stream"}
	startCalls := 0
	got, err := node.ensureStream("local-123", streamcfg.Options{NoControl: true}, func(string, streamcfg.Options) (*StreamSession, error) {
		startCalls++
		return fresh, nil
	})
	if err != nil {
		t.Fatalf("ensureStream returned error: %v", err)
	}
	if got != fresh {
		t.Fatalf("ensureStream returned %q, want the rebuilt session", got.ID)
	}
	if startCalls != 1 {
		t.Fatalf("start calls = %d, want 1", startCalls)
	}
}

func TestEnsureStreamAppliesExplicitTurnScreenOffWhenReusingExistingStream(t *testing.T) {
	done := make(chan struct{})
	close(done)

	controlConn := &recordingConn{}
	fake := &fakeADB{
		outputs: map[string][]byte{
			"": []byte("List of devices attached\nlocal-123\tdevice\n"),
		},
	}
	existing := &StreamSession{
		ID:           "existing-stream",
		DeviceSerial: "local-123",
		controlConn:  controlConn,
	}
	node := &Node{
		ID:             "local-node",
		AndroidEnabled: true,
		adb:            fake,
		streams: map[string]*streamEntry{
			"local-123": {
				Session: existing,
				Done:    done,
			},
		},
	}

	got, err := node.EnsureStream("local-123", streamcfg.Options{TurnScreenOff: true})
	if err != nil {
		t.Fatalf("EnsureStream returned error: %v", err)
	}
	if got != existing {
		t.Fatalf("EnsureStream returned %p, want existing %p", got, existing)
	}

	if want := []byte{scrcpy.SetDisplayPower, 0}; !cmp.Equal(controlConn.data, want) {
		t.Fatalf("control message mismatch (-want +got):\n%s", cmp.Diff(want, controlConn.data))
	}
}
