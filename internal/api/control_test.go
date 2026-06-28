package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brijorn/mast/internal/scrcpy"
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

	tapSerial string
	tapX      int
	tapY      int

	touchSerial string
	touchAction string
	touchX      int
	touchY      int

	swipeSerial string
	startX      int
	startY      int
	endX        int
	endY        int

	keySerial string
	keycode   uint32

	clipboardSerial string
	clipboardText   string

	err error
}

func (b *controlBackend) Tap(serial string, x int, y int) error {
	b.tapSerial = serial
	b.tapX = x
	b.tapY = y
	return b.err
}

func (b *controlBackend) Touch(serial string, action string, x int, y int) error {
	b.touchSerial = serial
	b.touchAction = action
	b.touchX = x
	b.touchY = y
	return b.err
}

func (b *controlBackend) Swipe(serial string, startX, startY, endX, endY int) error {
	b.swipeSerial = serial
	b.startX = startX
	b.startY = startY
	b.endX = endX
	b.endY = endY
	return b.err
}

func (b *controlBackend) PressKey(serial string, keycode uint32, metaState uint32) error {
	b.keySerial = serial
	b.keycode = keycode
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
