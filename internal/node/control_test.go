package node

import (
	"context"
	"math"
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

func TestTypeTextLocalUsesScrcpyControl(t *testing.T) {
	controlConn := &recordingConn{}
	node := newControlTestNode("local-node", "local-123")
	node.streams["local-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "local-123",
		controlConn:  controlConn,
	})

	if err := node.TypeText("local-123", "hello"); err != nil {
		t.Fatalf("TypeText returned error: %v", err)
	}

	want := []byte{scrcpy.InjectText, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'}
	if got := controlConn.data; string(got) != string(want) {
		t.Fatalf("text control message = %v, want %v", got, want)
	}
}

func TestTypeTextRemoteSendsPeerRequest(t *testing.T) {
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
		controlConn:  controlConn,
	})

	connectNodePair(t, nodeA, nodeB)

	if err := nodeA.TypeText("remote-123", "hello"); err != nil {
		t.Fatalf("TypeText returned error: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		return len(controlConn.data) == 10
	})
	want := []byte{scrcpy.InjectText, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'}
	if got := controlConn.data; string(got) != string(want) {
		t.Fatalf("text control message = %v, want %v", got, want)
	}
}

func TestIOSTextFromAndroidKeycode(t *testing.T) {
	tests := []struct {
		name      string
		keycode   uint32
		metaState uint32
		want      string
		wantOK    bool
	}{
		{name: "lowercase letter", keycode: 35, want: "g", wantOK: true},
		{name: "uppercase letter", keycode: 35, metaState: 0x0001, want: "G", wantOK: true},
		{name: "digit", keycode: 8, want: "1", wantOK: true},
		{name: "shifted digit", keycode: 8, metaState: 0x0001, want: "!", wantOK: true},
		{name: "space", keycode: 62, want: " ", wantOK: true},
		{name: "enter", keycode: 66, want: "\n", wantOK: true},
		{name: "comma", keycode: 55, want: ",", wantOK: true},
		{name: "shifted comma", keycode: 55, metaState: 0x0001, want: "<", wantOK: true},
		{name: "unsupported navigation", keycode: 19, wantOK: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := iosTextFromAndroidKeycode(test.keycode, test.metaState)
			if ok != test.wantOK {
				t.Fatalf("ok = %t, want %t", ok, test.wantOK)
			}
			if got != test.want {
				t.Fatalf("text = %q, want %q", got, test.want)
			}
		})
	}
}

func TestIOSTouchPathHelpers(t *testing.T) {
	points := appendIOSTouchPoint(nil, iosTouchPoint{X: 10, Y: 10})
	points = appendIOSTouchPoint(points, iosTouchPoint{X: 11, Y: 10})
	points = appendIOSTouchPoint(points, iosTouchPoint{X: 14, Y: 10})
	points = appendIOSTouchPoint(points, iosTouchPoint{X: 20, Y: 20})

	if len(points) != 3 {
		t.Fatalf("path point count = %d, want 3", len(points))
	}
	if points[1] != (iosTouchPoint{X: 14, Y: 10}) {
		t.Fatalf("second point = %+v, want 14,10", points[1])
	}
	if distance := iosTouchPathDistance(points); distance < 14 || distance > 15 {
		t.Fatalf("path distance = %f, want about 14.14", distance)
	}
}

func TestIOSDragDuration(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    float64
	}{
		{elapsed: 50 * time.Millisecond, want: 0.2},
		{elapsed: 450 * time.Millisecond, want: 0.45},
		{elapsed: 3 * time.Second, want: 2},
	}

	for _, test := range tests {
		got := iosDragDuration(test.elapsed)
		if math.Abs(got-test.want) > 0.001 {
			t.Fatalf("iosDragDuration(%s) = %f, want %f", test.elapsed, got, test.want)
		}
	}
}

func TestClipboardGetFromPeerReturnsRPCResponse(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	nodeB.streams["remote-123"] = readyStreamEntry(&StreamSession{
		DeviceSerial: "remote-123",
		controlConn:  server,
	})

	connectNodePair(t, nodeA, nodeB)

	messageCh := make(chan []byte, 1)
	go func() {
		getMessage := make([]byte, 2)
		_, _ = client.Read(getMessage)
		messageCh <- getMessage
		_, _ = client.Write([]byte{0, 0, 0, 0, 11})
		_, _ = client.Write([]byte("remote text"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	text, err := nodeA.getPeerClipboard(ctx, "b", "remote-123")
	if err != nil {
		t.Fatalf("getPeerClipboard returned error: %v", err)
	}
	if text != "remote text" {
		t.Fatalf("clipboard text = %q, want remote text", text)
	}

	getMessage := receiveControlMessage(t, messageCh)
	if getMessage[0] != scrcpy.GetClipboard || getMessage[1] != scrcpy.CopyKeyCopy {
		t.Fatalf("get clipboard message = %v", getMessage)
	}
}

func newControlTestNode(nodeID string, localSerial string) *Node {
	output := []byte("List of devices attached\n")
	if localSerial != "" {
		output = []byte("List of devices attached\n" + localSerial + "\tdevice\n")
	}

	return &Node{
		ID:             nodeID,
		AndroidEnabled: true,
		Peers:          map[string]*PeerConn{},
		adb:            &fakeADB{outputs: map[string][]byte{"": output}},
		streams:        map[string]*streamEntry{},
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
