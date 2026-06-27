package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brijorn/mast/internal/node"
)

func TestListNodesReturnsNodeInventory(t *testing.T) {
	backend := &nodesBackend{
		nodes: []node.NodeInfo{
			{
				ID:             "local-node",
				Local:          true,
				AndroidEnabled: true,
				ProxyEnabled:   true,
				Version:        "0.2.0",
				Commit:         "abc123",
				BuildDate:      "2026-06-25T17:00:00Z",
			},
			{
				ID:             "remote-node",
				Addr:           "100.64.0.2",
				Local:          false,
				AndroidEnabled: false,
				ProxyEnabled:   true,
				Version:        "0.1.0",
				Commit:         "def456",
				BuildDate:      "2026-06-24T17:00:00Z",
			},
		},
	}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var got []node.NodeInfo
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("len(response) = %d, want 2", len(got))
	}
	if got[1].ID != "remote-node" || got[1].Version != "0.1.0" || !got[1].ProxyEnabled {
		t.Fatalf("remote node = %+v", got[1])
	}
}

type nodesBackend struct {
	fakeBackend
	nodes []node.NodeInfo
}

func (b *nodesBackend) ListNodes() []node.NodeInfo {
	return b.nodes
}
