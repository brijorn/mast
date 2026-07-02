package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddPeerConnectsNormalizedTarget(t *testing.T) {
	backend := &peerBackend{}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers", bytes.NewReader([]byte(`{"target":"100.64.0.2"}`)))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.addr != "ws://100.64.0.2:6270/ws" {
		t.Fatalf("connected addr = %q", backend.addr)
	}

	var got addPeerResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.URL != "ws://100.64.0.2:6270/ws" {
		t.Fatalf("response URL = %q", got.URL)
	}
}

func TestAddPeerRejectsInvalidTarget(t *testing.T) {
	server := NewServer(&peerBackend{})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers", bytes.NewReader([]byte(`{"target":"http://100.64.0.2:6270/ws"}`)))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestAddPeerReturnsConnectError(t *testing.T) {
	server := NewServer(&peerBackend{err: errors.New("dial failed")})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers", bytes.NewReader([]byte(`{"target":"100.64.0.2"}`)))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
}

func TestRemovePeerDisconnectsNormalizedTarget(t *testing.T) {
	backend := &peerBackend{}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/peers", bytes.NewReader([]byte(`{"target":"100.64.0.2"}`)))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusNoContent, res.Body.String())
	}
	if backend.disconnectedAddr != "ws://100.64.0.2:6270/ws" {
		t.Fatalf("disconnected addr = %q", backend.disconnectedAddr)
	}
}

func TestRemovePeerRejectsInvalidTarget(t *testing.T) {
	server := NewServer(&peerBackend{})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/peers", bytes.NewReader([]byte(`{"target":"http://100.64.0.2:6270/ws"}`)))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

type peerBackend struct {
	fakeBackend
	addr             string
	disconnectedAddr string
	err              error
}

func (b *peerBackend) Connect(addr string) error {
	b.addr = addr
	return b.err
}

func (b *peerBackend) DisconnectPeer(addr string) bool {
	b.disconnectedAddr = addr
	return true
}
