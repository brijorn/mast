package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/brijorn/mast/internal/node"
)

func TestGetLocalRunDoesNotContactPeers(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", 503)
	}))
	defer peer.Close()
	server := NewServer(&fakeBackend{nodes: []node.NodeInfo{peerNode(t, peer.URL)}}, &fakeProgramBackend{})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/runs/run-1", nil))
	if response.Code != 200 || calls.Load() != 0 {
		t.Fatalf("status=%d, peer calls=%d", response.Code, calls.Load())
	}
}
