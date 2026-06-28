package node

import (
	"encoding/binary"
	"net"
	"testing"

	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/google/go-cmp/cmp"
)

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
