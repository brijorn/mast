package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/scrcpy"
	"github.com/gorilla/websocket"
)

func TestTapCallsBackend(t *testing.T) {
	backend := &controlBackend{}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","x":12,"y":34}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/tap", bytes.NewReader(body))

	server.Tap(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if backend.tapSerial != "local-123" || backend.tapX != 12 || backend.tapY != 34 {
		t.Fatalf("tap call = serial %q x %d y %d", backend.tapSerial, backend.tapX, backend.tapY)
	}
}

func TestTouchCallsBackend(t *testing.T) {
	backend := &controlBackend{}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","action":"move","x":12,"y":34}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/touch", bytes.NewReader(body))

	server.Touch(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if backend.touchSerial != "local-123" || backend.touchAction != "move" || backend.touchX != 12 || backend.touchY != 34 {
		t.Fatalf("touch call = serial %q action %q x %d y %d", backend.touchSerial, backend.touchAction, backend.touchX, backend.touchY)
	}
}

func TestControlWebSocketCallsBackend(t *testing.T) {
	backend := &controlBackend{touchDone: make(chan struct{}, 1)}
	server := httptest.NewServer(NewServer(backend).Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/api/control/ws?serial=local-123"), nil)
	if err != nil {
		t.Fatalf("dial control websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":   "touch",
		"action": "move",
		"x":      12,
		"y":      34,
	}); err != nil {
		t.Fatalf("write control message: %v", err)
	}

	select {
	case <-backend.touchDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for touch call")
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.touchSerial != "local-123" || backend.touchAction != "move" || backend.touchX != 12 || backend.touchY != 34 {
		t.Fatalf("touch call = serial %q action %q x %d y %d", backend.touchSerial, backend.touchAction, backend.touchX, backend.touchY)
	}
}

func TestControlWebSocketReturnsValidationError(t *testing.T) {
	server := httptest.NewServer(NewServer(&controlBackend{}).Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/api/control/ws?serial=local-123"), nil)
	if err != nil {
		t.Fatalf("dial control websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":   "touch",
		"action": "drag",
		"x":      12,
		"y":      34,
	}); err != nil {
		t.Fatalf("write control message: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	var res controlWSErrorResponse
	if err := conn.ReadJSON(&res); err != nil {
		t.Fatalf("read control error: %v", err)
	}
	if res.Type != "error" || res.Message != "action must be down, move, or up" {
		t.Fatalf("response = %#v", res)
	}
}

func TestControlWebSocketSwipeCallsBackend(t *testing.T) {
	backend := &controlBackend{swipeDone: make(chan struct{}, 1)}
	server := httptest.NewServer(NewServer(backend).Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/api/control/ws?serial=local-123"), nil)
	if err != nil {
		t.Fatalf("dial control websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":    "swipe",
		"start_x": 12,
		"start_y": 34,
		"end_x":   56,
		"end_y":   78,
	}); err != nil {
		t.Fatalf("write control message: %v", err)
	}

	select {
	case <-backend.swipeDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for swipe call")
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.swipeSerial != "local-123" || backend.startX != 12 || backend.startY != 34 || backend.endX != 56 || backend.endY != 78 {
		t.Fatalf("swipe call = serial %q start %d,%d end %d,%d", backend.swipeSerial, backend.startX, backend.startY, backend.endX, backend.endY)
	}
}

func TestControlWebSocketPreservesControlOrder(t *testing.T) {
	backend := &controlBackend{
		touchDone:  make(chan struct{}, 2),
		blockTouch: make(chan struct{}),
	}
	server := httptest.NewServer(NewServer(backend).Handler())
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/api/control/ws?serial=local-123"), nil)
	if err != nil {
		t.Fatalf("dial control websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":   "touch",
		"action": "down",
		"x":      12,
		"y":      34,
	}); err != nil {
		t.Fatalf("write first control message: %v", err)
	}
	if err := conn.WriteJSON(map[string]any{
		"type":   "touch",
		"action": "up",
		"x":      56,
		"y":      78,
	}); err != nil {
		t.Fatalf("write second control message: %v", err)
	}

	select {
	case <-backend.touchDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first touch call")
	}

	time.Sleep(50 * time.Millisecond)
	backend.mu.Lock()
	callCount := len(backend.touchCalls)
	backend.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("touch calls while first call is blocked = %d, want 1", callCount)
	}

	close(backend.blockTouch)

	select {
	case <-backend.touchDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second touch call")
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if strings.Join(backend.touchCalls, ",") != "down,up" {
		t.Fatalf("touch call order = %q, want down,up", strings.Join(backend.touchCalls, ","))
	}
}

func TestSwipeCallsBackend(t *testing.T) {
	backend := &controlBackend{}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","start_x":12,"start_y":34,"end_x":56,"end_y":78}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/swipe", bytes.NewReader(body))

	server.Swipe(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if backend.swipeSerial != "local-123" || backend.startX != 12 || backend.startY != 34 || backend.endX != 56 || backend.endY != 78 {
		t.Fatalf("swipe call = serial %q start %d,%d end %d,%d", backend.swipeSerial, backend.startX, backend.startY, backend.endX, backend.endY)
	}
}

func TestPressKeyCallsBackend(t *testing.T) {
	backend := &controlBackend{}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","keycode":4}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/keypress", bytes.NewReader(body))

	server.PressKey(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if backend.keySerial != "local-123" || backend.keycode != scrcpy.KeycodeBack {
		t.Fatalf("press key call = serial %q keycode %d", backend.keySerial, backend.keycode)
	}
}

func TestPressKeyRejectsInvalidKeycode(t *testing.T) {
	server := NewServer(&controlBackend{})

	body := []byte(`{"serial":"local-123","keycode":999}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/keypress", bytes.NewReader(body))

	server.PressKey(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestPressButtonCallsBackend(t *testing.T) {
	backend := &controlBackend{}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","name":"home"}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/button", bytes.NewReader(body))

	server.PressButton(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if backend.buttonSerial != "local-123" || backend.buttonName != "home" {
		t.Fatalf("press button call = serial %q name %q", backend.buttonSerial, backend.buttonName)
	}
}

func TestTypeTextCallsBackend(t *testing.T) {
	backend := &controlBackend{}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","text":"hello iOS"}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/text", bytes.NewReader(body))

	server.TypeText(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if backend.textSerial != "local-123" || backend.text != "hello iOS" {
		t.Fatalf("type text call = serial %q text %q", backend.textSerial, backend.text)
	}
}

func TestGetClipboardReturnsBackendText(t *testing.T) {
	backend := &controlBackend{clipboardText: "copied text"}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123"}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/clipboard/get", bytes.NewReader(body))

	server.GetClipboard(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if res.Body.String() != "{\"text\":\"copied text\"}\n" {
		t.Fatalf("body = %q", res.Body.String())
	}
	if backend.clipboardSerial != "local-123" {
		t.Fatalf("clipboard serial = %q", backend.clipboardSerial)
	}
}

func TestSetClipboardCallsBackend(t *testing.T) {
	backend := &controlBackend{}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","text":"paste text"}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/clipboard/set", bytes.NewReader(body))

	server.SetClipboard(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if backend.clipboardSerial != "local-123" || backend.clipboardText != "paste text" {
		t.Fatalf("clipboard call = serial %q text %q", backend.clipboardSerial, backend.clipboardText)
	}
}

func TestTapRejectsInvalidRequest(t *testing.T) {
	server := NewServer(&controlBackend{})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/tap", bytes.NewReader([]byte(`{"x":12,"y":34}`)))

	server.Tap(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestSwipeReturnsBackendError(t *testing.T) {
	server := NewServer(&controlBackend{err: errors.New("boom")})

	body := []byte(`{"serial":"local-123","start_x":12,"start_y":34,"end_x":56,"end_y":78}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/control/swipe", bytes.NewReader(body))

	server.Swipe(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
}

type controlBackend struct {
	fakeBackend
	mu sync.Mutex

	tapSerial string
	tapX      int
	tapY      int

	touchSerial string
	touchAction string
	touchX      int
	touchY      int
	touchCalls  []string

	swipeSerial string
	startX      int
	startY      int
	endX        int
	endY        int

	keySerial string
	keycode   uint32

	buttonSerial string
	buttonName   string

	textSerial string
	text       string

	clipboardSerial string
	clipboardText   string

	touchDone  chan struct{}
	blockTouch chan struct{}
	swipeDone  chan struct{}
	err        error
}

func (b *controlBackend) Tap(serial string, x int, y int) error {
	b.tapSerial = serial
	b.tapX = x
	b.tapY = y
	return b.err
}

func (b *controlBackend) Touch(serial string, action string, x int, y int) error {
	b.mu.Lock()
	b.touchSerial = serial
	b.touchAction = action
	b.touchX = x
	b.touchY = y
	b.touchCalls = append(b.touchCalls, action)
	b.mu.Unlock()
	if b.touchDone != nil {
		b.touchDone <- struct{}{}
	}
	if b.blockTouch != nil {
		<-b.blockTouch
	}
	return b.err
}

func (b *controlBackend) Swipe(serial string, startX, startY, endX, endY int) error {
	b.mu.Lock()
	b.swipeSerial = serial
	b.startX = startX
	b.startY = startY
	b.endX = endX
	b.endY = endY
	b.mu.Unlock()
	if b.swipeDone != nil {
		b.swipeDone <- struct{}{}
	}
	return b.err
}

func (b *controlBackend) PressKey(serial string, keycode uint32, metaState uint32) error {
	b.keySerial = serial
	b.keycode = keycode
	return b.err
}

func (b *controlBackend) PressButton(serial string, name string) error {
	b.buttonSerial = serial
	b.buttonName = name
	return b.err
}

func (b *controlBackend) TypeText(serial string, text string) error {
	b.textSerial = serial
	b.text = text
	return b.err
}

func (b *controlBackend) GetClipboard(serial string) (string, error) {
	b.clipboardSerial = serial
	return b.clipboardText, b.err
}

func (b *controlBackend) SetClipboard(serial string, text string) error {
	b.clipboardSerial = serial
	b.clipboardText = text
	return b.err
}

func wsURL(serverURL string, path string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http") + path
}
