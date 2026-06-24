package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/brijorn/mast/internal/node"
	streamcfg "github.com/brijorn/mast/internal/stream"
)

type fakeBackend struct {
	mu sync.Mutex

	session *node.StreamSession
	err     error
	calls   int
	serials []string
	options []streamcfg.Options

	started chan struct{}
	release chan struct{}
}

func (f *fakeBackend) ListDevices() ([]node.DeviceInfo, error) {
	return nil, nil
}

func (f *fakeBackend) EnsureStream(serial string, opts streamcfg.Options) (*node.StreamSession, error) {
	f.mu.Lock()
	f.calls++
	f.serials = append(f.serials, serial)
	f.options = append(f.options, opts)
	if f.started != nil && f.calls == 1 {
		close(f.started)
	}
	f.mu.Unlock()

	if f.release != nil {
		<-f.release
	}

	return f.session, f.err
}

func (f *fakeBackend) Tap(_ string, _, _ int) error {
	return nil
}

func (f *fakeBackend) Swipe(_ string, _, _, _, _ int) error {
	return nil
}

func (f *fakeBackend) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestStartStreamStartsStream(t *testing.T) {
	backend := &fakeBackend{
		session: &node.StreamSession{
			ID:           "stream-1",
			DeviceSerial: "local-123",
			Host:         "100.64.0.1",
			LocalPort:    12345,
		},
	}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","options":{"no_audio":true,"max_size":1080}}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/streams", bytes.NewReader(body))

	server.StartStream(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}

	var got startStreamResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	expected := startStreamResponse{
		ID:        "stream-1",
		Serial:    "local-123",
		Host:      "100.64.0.1",
		LocalPort: 12345,
	}
	if got != expected {
		t.Fatalf("response = %+v, want %+v", got, expected)
	}

	if backend.callCount() != 1 {
		t.Fatalf("EnsureStream calls = %d, want 1", backend.callCount())
	}
	if got := backend.options[0]; !got.NoAudio || got.MaxSize != 1080 {
		t.Fatalf("options = %+v, want no_audio=true and max_size=1080", got)
	}
}

func TestStartStreamReturnsEnsuredStream(t *testing.T) {
	backend := &fakeBackend{
		session: &node.StreamSession{
			ID:        "existing-stream",
			Host:      "100.64.0.2",
			LocalPort: 23456,
		},
	}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123"}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/streams", bytes.NewReader(body))

	server.StartStream(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.callCount() != 1 {
		t.Fatalf("EnsureStream calls = %d, want 1", backend.callCount())
	}
}

func TestStartStreamRequiresSerial(t *testing.T) {
	server := NewServer(&fakeBackend{})
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/streams", bytes.NewReader([]byte(`{}`)))

	server.StartStream(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestStartStreamReturnsBackendError(t *testing.T) {
	expectedErr := errors.New("start failed")
	backend := &fakeBackend{err: expectedErr}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123"}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/streams", bytes.NewReader(body))

	server.StartStream(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
}
