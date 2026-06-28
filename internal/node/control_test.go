package node

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/scrcpy"
)

type recordingConn struct {
	data []byte
}

func (c *recordingConn) Read([]byte) (int, error) { return 0, nil }
func (c *recordingConn) Write(p []byte) (int, error) {
	c.data = append(c.data, p...)
	return len(p), nil
}
func (c *recordingConn) Close() error                     { return nil }
func (c *recordingConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (c *recordingConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (c *recordingConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return string(a) }
func (a fakeAddr) String() string  { return string(a) }

func TestTapLocalWritesControlMessage(t *testing.T) {
	controlConn := &recordingConn{}
	node := newControlTestNode("local-node", "local-123")
	node.streams["local-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "local-123",
		Width:        944,
		Height:       1080,
		controlConn:  controlConn,
	})

	if err := node.Tap("local-123", 12, 34); err != nil {
		t.Fatalf("Tap returned error: %v", err)
	}

	if len(controlConn.data) != 64 {
		t.Fatalf("tap wrote %d bytes, want 64", len(controlConn.data))
	}
	if controlConn.data[0] != scrcpy.InjectTouchEvent {
		t.Fatalf("down message type = %d, want %d", controlConn.data[0], scrcpy.InjectTouchEvent)
	}
	if controlConn.data[1] != scrcpy.ActionDown {
		t.Fatalf("down action = %d, want %d", controlConn.data[1], scrcpy.ActionDown)
	}
	if controlConn.data[33] != scrcpy.ActionUp {
		t.Fatalf("up action = %d, want %d", controlConn.data[33], scrcpy.ActionUp)
	}
}

func TestTapLocalRequiresStartedStream(t *testing.T) {
	node := newControlTestNode("local-node", "local-123")

	err := node.Tap("local-123", 12, 34)
	if err == nil || !strings.Contains(err.Error(), "stream not found") {
		t.Fatalf("Tap error = %v, want stream not found", err)
	}
}

func TestTapLocalRequiresControlConnection(t *testing.T) {
	node := newControlTestNode("local-node", "local-123")
	node.streams["local-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "local-123",
		Width:        944,
		Height:       1080,
	})

	err := node.Tap("local-123", 12, 34)
	if err == nil || !strings.Contains(err.Error(), "stream control connection not available") {
		t.Fatalf("Tap error = %v, want missing control connection", err)
	}
}

func TestTapRemoteSendsPeerRequest(t *testing.T) {
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
	}
	nodeB.AndroidEnabled = true
	controlConn := &recordingConn{}
	nodeB.streams["remote-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "remote-123",
		Width:        944,
		Height:       1080,
		controlConn:  controlConn,
	})

	connectNodePair(t, nodeA, nodeB)

	if err := nodeA.Tap("remote-123", 12, 34); err != nil {
		t.Fatalf("Tap returned error: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		return len(controlConn.data) == 64
	})
	if len(controlConn.data) != 64 {
		t.Fatalf("tap wrote %d bytes, want 64", len(controlConn.data))
	}
	if controlConn.data[0] != scrcpy.InjectTouchEvent {
		t.Fatalf("down message type = %d, want %d", controlConn.data[0], scrcpy.InjectTouchEvent)
	}
}

func TestSwipeLocalWritesControlMessage(t *testing.T) {
	controlConn := &recordingConn{}
	node := newControlTestNode("local-node", "local-123")
	node.streams["local-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "local-123",
		Width:        944,
		Height:       1080,
		controlConn:  controlConn,
	})

	if err := node.Swipe("local-123", 12, 34, 56, 78); err != nil {
		t.Fatalf("Swipe returned error: %v", err)
	}

	if len(controlConn.data) != 320 {
		t.Fatalf("swipe wrote %d bytes, want 320", len(controlConn.data))
	}
	if controlConn.data[1] != scrcpy.ActionDown {
		t.Fatalf("down action = %d, want %d", controlConn.data[1], scrcpy.ActionDown)
	}
	if controlConn.data[33] != scrcpy.ActionMove {
		t.Fatalf("move action = %d, want %d", controlConn.data[33], scrcpy.ActionMove)
	}
	if controlConn.data[len(controlConn.data)-31] != scrcpy.ActionUp {
		t.Fatalf("up action = %d, want %d", controlConn.data[len(controlConn.data)-31], scrcpy.ActionUp)
	}
}

func TestSwipeRemoteSendsPeerRequest(t *testing.T) {
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
	}
	nodeB.AndroidEnabled = true
	controlConn := &recordingConn{}
	nodeB.streams["remote-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "remote-123",
		Width:        944,
		Height:       1080,
		controlConn:  controlConn,
	})

	connectNodePair(t, nodeA, nodeB)

	if err := nodeA.Swipe("remote-123", 12, 34, 56, 78); err != nil {
		t.Fatalf("Swipe returned error: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		return len(controlConn.data) == 320
	})
	if len(controlConn.data) != 320 {
		t.Fatalf("swipe wrote %d bytes, want 320", len(controlConn.data))
	}
	if controlConn.data[1] != scrcpy.ActionDown {
		t.Fatalf("down action = %d, want %d", controlConn.data[1], scrcpy.ActionDown)
	}
}

func TestClipboardLocalUsesScrcpyControl(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	node := newControlTestNode("local-node", "local-123")
	node.streams["local-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "local-123",
		controlConn:  server,
	})

	messageCh := make(chan []byte, 2)
	go func() {
		getMessage := make([]byte, 2)
		_, _ = client.Read(getMessage)
		messageCh <- getMessage
		_, _ = client.Write([]byte{0, 0, 0, 0, 10})
		_, _ = client.Write([]byte("phone text"))

		setHeader := make([]byte, 14)
		_, _ = client.Read(setHeader)
		textLen := int(setHeader[10])<<24 | int(setHeader[11])<<16 | int(setHeader[12])<<8 | int(setHeader[13])
		setText := make([]byte, textLen)
		_, _ = client.Read(setText)
		messageCh <- setHeader
	}()

	text, err := node.GetClipboard("local-123")
	if err != nil {
		t.Fatalf("GetClipboard returned error: %v", err)
	}
	if text != "phone text" {
		t.Fatalf("clipboard text = %q", text)
	}

	getMessage := receiveControlMessage(t, messageCh)
	if getMessage[0] != scrcpy.GetClipboard || getMessage[1] != scrcpy.CopyKeyCopy {
		t.Fatalf("get clipboard message = %v", getMessage)
	}

	if err := node.SetClipboard("local-123", "desktop text"); err != nil {
		t.Fatalf("SetClipboard returned error: %v", err)
	}

	setHeader := receiveControlMessage(t, messageCh)
	if setHeader[0] != scrcpy.SetClipboard {
		t.Fatalf("set clipboard type = %d", setHeader[0])
	}
	if setHeader[9] != 1 {
		t.Fatalf("set clipboard paste flag = %d, want 1", setHeader[9])
	}
}

func newControlTestNode(nodeID string, localSerial string) *Node {
	output := []byte("List of devices attached\n")
	if localSerial != "" {
		output = []byte("List of devices attached\n" + localSerial + "\tdevice\n")
	}

	return &Node{
		ID:      nodeID,
		Peers:   map[string]*PeerConn{},
		adb:     &fakeADB{outputs: map[string][]byte{"": output}},
		streams: map[string]*streamEntry{},
	}
}

func readyStreamEntry(session *StreamSession) *streamEntry {
	done := make(chan struct{})
	close(done)
	return &streamEntry{
		Session: session,
		Done:    done,
	}
}

func receiveControlMessage(t *testing.T, messageCh <-chan []byte) []byte {
	t.Helper()

	select {
	case message := <-messageCh:
		return message
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for control request")
		return nil
	}
}
