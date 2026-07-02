package node

import (
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

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

func TestEnsureStreamReusesExistingStreamWithStaleReplayKeyframe(t *testing.T) {
	node := &Node{
		streams: make(map[string]*streamEntry),
	}
	done := make(chan struct{})
	close(done)

	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Keyframe: true, Data: []byte{1}})
	broadcaster.latestKeyframe.receivedAt = time.Now().Add(-videoReplayKeyframeMaxAge - time.Second)

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

func TestEnsureStreamTurnsScreenOffWhenReusingExistingStream(t *testing.T) {
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

	got, err := node.EnsureStream("local-123", streamcfg.Options{})
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
