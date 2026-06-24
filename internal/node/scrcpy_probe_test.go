package node

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"os"
	"testing"
	"time"

	streamcfg "github.com/brijorn/mast/internal/stream"
)

func TestProbeScrcpyHandshake(t *testing.T) {
	serial := os.Getenv("MAST_TEST_ANDROID_SERIAL")
	if serial == "" {
		t.Skip("set MAST_TEST_ANDROID_SERIAL env var")
	}

	n := &Node{
		ID:            "probe-node",
		AdvertiseHost: "127.0.0.1",
		Peers:         map[string]*PeerConn{},
		adb:           realADB{},
	}

	device, err := n.deviceBySerial(serial)
	if err != nil {
		t.Fatal(err)
	}

	host, err := n.adbHostForNode(device.NodeID)
	if err != nil {
		t.Fatal(err)
	}

	if err := n.pushScrcpyServer(host); err != nil {
		t.Fatal(err)
	}

	ln, port, err := newScrcpyListener()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Logf("close listener: %v", err)
		}
	}()

	if err := n.createScrcpyReverse(host, port); err != nil {
		t.Fatal(err)
	}

	cmd, err := n.startScrcpyProcess(host, streamcfg.Options{NoAudio: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			if err := cmd.Process.Kill(); err != nil {
				t.Logf("kill scrcpy server: %v", err)
			}
		}
		if err := cmd.Wait(); err != nil {
			t.Logf("wait scrcpy server: %v", err)
		}
	}()

	videoConn := acceptProbeConn(t, ln, "video")
	defer closeProbeConn(t, videoConn, "video")

	controlConn := acceptProbeConn(t, ln, "control")
	defer closeProbeConn(t, controlConn, "control")

	deviceName := make([]byte, 64)
	if err := videoConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(videoConn, deviceName); err != nil {
		t.Fatal(err)
	}

	name := string(bytes.TrimRight(deviceName, "\x00"))
	t.Logf("video socket device name: %q", name)
	t.Logf("video socket device-name bytes:\n%s", hex.Dump(deviceName))

	streamMeta := make([]byte, 16)
	if err := videoConn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		t.Fatal(err)
	}
	nread, err := io.ReadFull(videoConn, streamMeta)
	if err != nil {
		t.Logf("video socket did not produce full stream metadata yet: read=%d err=%v", nread, err)
		return
	}

	codec := string(bytes.TrimLeft(streamMeta[:4], "\x00"))
	flags := binary.BigEndian.Uint32(streamMeta[4:8])
	width := binary.BigEndian.Uint32(streamMeta[8:12])
	height := binary.BigEndian.Uint32(streamMeta[12:16])

	t.Logf("video socket codec: %q", codec)
	t.Logf("video socket session flags: 0x%08x", flags)
	t.Logf("video socket dimensions: %dx%d", width, height)
	t.Logf("video socket stream metadata bytes:\n%s", hex.Dump(streamMeta))
}

func acceptProbeConn(t *testing.T, ln net.Listener, name string) net.Conn {
	t.Helper()

	if deadlineSetter, ok := ln.(interface{ SetDeadline(time.Time) error }); ok {
		if err := deadlineSetter.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept %s socket: %v", name, err)
	}

	t.Logf("accepted %s socket from %s", name, conn.RemoteAddr())
	return conn
}

func closeProbeConn(t *testing.T, conn net.Conn, name string) {
	t.Helper()

	if err := conn.Close(); err != nil {
		t.Logf("close %s socket: %v", name, err)
	}
}
