package node

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/scrcpy"
	"github.com/brijorn/mast/internal/transport"
	"github.com/gorilla/websocket"
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
	messageCh := make(chan []byte, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, message, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read message: %v", err)
			return
		}
		messageCh <- message
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	node := newControlTestNode("local-node", "")
	node.adb = &fakeADB{
		outputs: map[string][]byte{
			"":         []byte("List of devices attached\n"),
			"10.0.0.2": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}
	node.Peers["remote-node"] = &PeerConn{
		conn:           conn,
		AndroidEnabled: true,
		Addr:           "10.0.0.2",
	}

	if err := node.Tap("remote-123", 12, 34); err != nil {
		t.Fatalf("Tap returned error: %v", err)
	}

	var raw transport.RawMessage
	message := receiveControlMessage(t, messageCh)
	if err := json.Unmarshal(message, &raw); err != nil {
		t.Fatalf("decode raw message: %v", err)
	}
	if raw.Type != transport.TypeTapRequest {
		t.Fatalf("message type = %q, want %q", raw.Type, transport.TypeTapRequest)
	}
	if raw.From != "local-node" {
		t.Fatalf("from = %q, want local-node", raw.From)
	}
	if raw.To != "remote-node" {
		t.Fatalf("to = %q, want remote-node", raw.To)
	}

	var req transport.TapRequest
	if err := json.Unmarshal(message, &req); err != nil {
		t.Fatalf("decode tap request: %v", err)
	}
	expected := transport.TapRequestPayload{Serial: "remote-123", X: 12, Y: 34}
	if req.Payload != expected {
		t.Fatalf("payload = %+v, want %+v", req.Payload, expected)
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
	messageCh := make(chan []byte, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, message, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read message: %v", err)
			return
		}
		messageCh <- message
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	node := newControlTestNode("local-node", "")
	node.adb = &fakeADB{
		outputs: map[string][]byte{
			"":         []byte("List of devices attached\n"),
			"10.0.0.2": []byte("List of devices attached\nremote-123\tdevice\n"),
		},
	}
	node.Peers["remote-node"] = &PeerConn{
		conn:           conn,
		AndroidEnabled: true,
		Addr:           "10.0.0.2",
	}

	if err := node.Swipe("remote-123", 12, 34, 56, 78); err != nil {
		t.Fatalf("Swipe returned error: %v", err)
	}

	message := receiveControlMessage(t, messageCh)
	var raw transport.RawMessage
	if err := json.Unmarshal(message, &raw); err != nil {
		t.Fatalf("decode raw message: %v", err)
	}
	if raw.Type != transport.TypeSwipeRequest {
		t.Fatalf("message type = %q, want %q", raw.Type, transport.TypeSwipeRequest)
	}

	var req transport.SwipeRequest
	if err := json.Unmarshal(message, &req); err != nil {
		t.Fatalf("decode swipe request: %v", err)
	}
	expected := transport.SwipeRequestPayload{Serial: "remote-123", StartX: 12, StartY: 34, EndX: 56, EndY: 78}
	if req.Payload != expected {
		t.Fatalf("payload = %+v, want %+v", req.Payload, expected)
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
