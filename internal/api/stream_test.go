package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/node"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/brijorn/mast/internal/update"
	"github.com/gorilla/websocket"
)

type fakeBackend struct {
	mu sync.Mutex

	session        *node.StreamSession
	err            error
	devices        []node.DeviceInfo
	dns            *node.DeviceDNSStatus
	dnsSet         *node.DeviceDNSStatus
	orientation    *node.DeviceOrientationStatus
	orientationSet node.DeviceOrientation
	calls          int
	serials        []string
	options        []streamcfg.Options
	stopped        []string

	started      chan struct{}
	release      chan struct{}
	videoStarts  chan string
	videoCancels chan string
}

func (f *fakeBackend) ListDevices() ([]node.DeviceInfo, error) {
	return f.devices, nil
}

func (f *fakeBackend) Screenshot(_ string) ([]byte, error) {
	return nil, nil
}

func (f *fakeBackend) DeviceDNS(serial string) (*node.DeviceDNSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serials = append(f.serials, serial)
	return f.dns, f.err
}

func (f *fakeBackend) SetDeviceDNS(serial string, desired node.DeviceDNSStatus) (*node.DeviceDNSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serials = append(f.serials, serial)
	f.dnsSet = &desired
	return f.dns, f.err
}

func (f *fakeBackend) SetDeviceOrientation(serial string, orientation node.DeviceOrientation) (*node.DeviceOrientationStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serials = append(f.serials, serial)
	f.orientationSet = orientation
	return f.orientation, f.err
}

func (f *fakeBackend) ListNodes() []node.NodeInfo {
	return nil
}

func (f *fakeBackend) Connect(_ string) error {
	return nil
}

func (f *fakeBackend) DisconnectPeer(_ string) bool {
	return false
}

func (f *fakeBackend) CheckNodeUpdate(_ context.Context, _ string) (*update.CheckResult, error) {
	return nil, nil
}

func (f *fakeBackend) ApplyNodeUpdate(_ context.Context, _ string, _ update.ApplyOptions) (*update.ApplyResult, error) {
	return nil, nil
}

func (f *fakeBackend) GetNodeConfig(_ context.Context, _ string) (*mastconfig.Config, error) {
	return nil, nil
}

func (f *fakeBackend) UpdateNodeConfig(_ context.Context, _ string, _ map[string]string) (*mastconfig.UpdateResult, error) {
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

func (f *fakeBackend) GetStream(_ string) (*node.StreamSession, error) {
	return f.session, f.err
}

func (f *fakeBackend) StopStream(serial string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, serial)
	return f.err
}

func (f *fakeBackend) DropStream(_ string, _ *node.StreamSession) {}

func (f *fakeBackend) StreamMJPEG(_ context.Context, serial string, w http.ResponseWriter) error {
	f.mu.Lock()
	f.serials = append(f.serials, serial)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	_, _ = w.Write([]byte("--frame\r\n"))
	return nil
}

func (f *fakeBackend) StreamVideo(ctx context.Context, serial string, conn *websocket.Conn) error {
	f.mu.Lock()
	f.serials = append(f.serials, serial)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if f.videoStarts != nil {
		f.videoStarts <- serial
		<-ctx.Done()
		f.videoCancels <- serial
		return ctx.Err()
	}
	return conn.WriteMessage(websocket.BinaryMessage, []byte("video"))
}

func TestStreamVideoClientCloseCancelsViewer(t *testing.T) {
	backend := &fakeBackend{
		videoStarts:  make(chan string, 1),
		videoCancels: make(chan string, 1),
	}
	server := httptest.NewServer(NewServer(backend).Handler())
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/streams/video?serial=phone-1&viewer=viewer-1"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial video websocket: %v", err)
	}

	select {
	case <-backend.videoStarts:
	case <-time.After(time.Second):
		t.Fatal("video viewer did not start")
	}
	if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		t.Fatalf("close video websocket: %v", err)
	}
	_ = conn.Close()

	select {
	case <-backend.videoCancels:
	case <-time.After(time.Second):
		t.Fatal("video viewer was not canceled after the client closed")
	}
}

func TestStreamVideoMissingStreamUsesTypedCloseCode(t *testing.T) {
	backend := &fakeBackend{err: node.ErrStreamNotFound}
	server := httptest.NewServer(NewServer(backend).Handler())
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/streams/video?serial=phone-1&viewer=viewer-1"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial video websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("ReadMessage error = %v, want websocket close error", err)
	}
	if closeErr.Code != node.VideoCloseStreamNotFound {
		t.Fatalf("close code = %d, want %d", closeErr.Code, node.VideoCloseStreamNotFound)
	}
}

func TestStreamVideoNewViewerReplacesPreviousViewer(t *testing.T) {
	backend := &fakeBackend{
		videoStarts:  make(chan string, 2),
		videoCancels: make(chan string, 2),
	}
	server := httptest.NewServer(NewServer(backend).Handler())
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/streams/video?serial=phone-1&viewer=viewer-1"
	first, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial first video websocket: %v", err)
	}
	defer func() { _ = first.Close() }()
	select {
	case <-backend.videoStarts:
	case <-time.After(time.Second):
		t.Fatal("first video viewer did not start")
	}

	second, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial replacement video websocket: %v", err)
	}
	defer func() { _ = second.Close() }()
	select {
	case <-backend.videoStarts:
	case <-time.After(time.Second):
		t.Fatal("replacement video viewer did not start")
	}
	select {
	case <-backend.videoCancels:
	case <-time.After(time.Second):
		t.Fatal("previous video viewer was not canceled")
	}
}

func TestStreamVideoDifferentViewersRemainConnected(t *testing.T) {
	backend := &fakeBackend{
		videoStarts:  make(chan string, 2),
		videoCancels: make(chan string, 2),
	}
	server := httptest.NewServer(NewServer(backend).Handler())
	defer server.Close()
	baseURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/streams/video?serial=phone-1&viewer="

	first, _, err := websocket.DefaultDialer.Dial(baseURL+"viewer-1", nil)
	if err != nil {
		t.Fatalf("dial first viewer: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, _, err := websocket.DefaultDialer.Dial(baseURL+"viewer-2", nil)
	if err != nil {
		t.Fatalf("dial second viewer: %v", err)
	}
	defer func() { _ = second.Close() }()

	for range 2 {
		select {
		case <-backend.videoStarts:
		case <-time.After(time.Second):
			t.Fatal("video viewer did not start")
		}
	}
	select {
	case <-backend.videoCancels:
		t.Fatal("an independent video viewer was canceled")
	case <-time.After(100 * time.Millisecond):
	}
}

func (f *fakeBackend) Touch(_ string, _ string, _, _ int) error {
	return nil
}

func (f *fakeBackend) Tap(_ string, _, _ int) error {
	return nil
}

func (f *fakeBackend) Swipe(_ string, _, _, _, _ int) error {
	return nil
}

func (f *fakeBackend) PressKey(_ string, _ uint32, _ uint32) error {
	return nil
}

func (f *fakeBackend) PressButton(_ string, _ string) error {
	return nil
}

func (f *fakeBackend) TypeText(_ string, _ string) error {
	return nil
}

func (f *fakeBackend) GetClipboard(_ string) (string, error) {
	return "", nil
}

func (f *fakeBackend) SetClipboard(_ string, _ string) error {
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
			Platform:     node.PlatformAndroid,
			Kind:         "h264",
			Host:         "100.64.0.1",
			LocalPort:    12345,
			VideoURL:     "/api/streams/video?serial=local-123",
		},
	}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","options":{"no_audio":true,"max_size":1080,"preserve_orientation":true}}`)
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
		Platform:  node.PlatformAndroid,
		Kind:      "h264",
		Host:      "100.64.0.1",
		LocalPort: 12345,
		VideoURL:  "/api/streams/video?serial=local-123",
	}
	if got != expected {
		t.Fatalf("response = %+v, want %+v", got, expected)
	}

	if backend.callCount() != 1 {
		t.Fatalf("EnsureStream calls = %d, want 1", backend.callCount())
	}
	if got := backend.options[0]; !got.NoAudio || got.MaxSize != 1080 || !got.TurnScreenOff || !got.PreserveOrientation {
		t.Fatalf("options = %+v, want no_audio=true, max_size=1080, turn_screen_off=true, and preserve_orientation=true", got)
	}
}

func TestStartStreamReturnsIOSMJPEGStream(t *testing.T) {
	backend := &fakeBackend{
		session: &node.StreamSession{
			ID:           "ios-stream-1",
			DeviceSerial: "ios-123",
			Platform:     node.PlatformIOS,
			Kind:         "mjpeg",
			Host:         "100.64.0.9",
			MJPEGURL:     "/api/streams/mjpeg?serial=ios-123",
			Width:        390,
			Height:       844,
		},
	}
	server := NewServer(backend)

	body := []byte(`{"serial":"ios-123"}`)
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
		ID:       "ios-stream-1",
		Serial:   "ios-123",
		Platform: node.PlatformIOS,
		Kind:     "mjpeg",
		Host:     "100.64.0.9",
		MJPEGURL: "/api/streams/mjpeg?serial=ios-123",
		Width:    390,
		Height:   844,
	}
	if got != expected {
		t.Fatalf("response = %+v, want %+v", got, expected)
	}
}

func TestStreamMJPEGDelegatesToBackend(t *testing.T) {
	backend := &fakeBackend{}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/streams/mjpeg?serial=ios-123", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "multipart/x-mixed-replace; boundary=frame" {
		t.Fatalf("Content-Type = %q, want MJPEG", got)
	}
	if got := res.Body.String(); got != "--frame\r\n" {
		t.Fatalf("body = %q, want frame marker", got)
	}
	if len(backend.serials) != 1 || backend.serials[0] != "ios-123" {
		t.Fatalf("serials = %+v, want ios-123", backend.serials)
	}
}

func TestStreamMJPEGMissingStreamReturnsNotFound(t *testing.T) {
	backend := &fakeBackend{err: node.ErrStreamNotFound}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/streams/mjpeg?serial=ios-123", nil)
	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNotFound, res.Body.String())
	}
}

func TestStopStreamStopsSerial(t *testing.T) {
	backend := &fakeBackend{}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/streams/local-123", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if len(backend.stopped) != 1 || backend.stopped[0] != "local-123" {
		t.Fatalf("stopped = %#v, want local-123", backend.stopped)
	}
}

func TestStopStreamTreatsMissingStreamAsStopped(t *testing.T) {
	backend := &fakeBackend{err: errors.New("stream not found: local-123")}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/streams/local-123", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
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
	if got := backend.options[0]; !got.TurnScreenOff {
		t.Fatalf("TurnScreenOff = false, want true by default")
	}
}

func TestStartStreamDoesNotDefaultScreenOffWithoutControl(t *testing.T) {
	backend := &fakeBackend{
		session: &node.StreamSession{
			ID:        "stream-no-control",
			Host:      "100.64.0.2",
			LocalPort: 23456,
		},
	}
	server := NewServer(backend)

	body := []byte(`{"serial":"local-123","options":{"no_control":true}}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/streams", bytes.NewReader(body))

	server.StartStream(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if got := backend.options[0]; got.TurnScreenOff {
		t.Fatalf("TurnScreenOff = true, want false when no_control=true")
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
