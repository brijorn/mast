package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/brijorn/mast/internal/node"
)

// peerNode turns a test server's URL into a NodeInfo the aggregation code can
// dial, splitting the host and port the way a real peer advertises them.
func peerNode(t *testing.T, serverURL string) node.NodeInfo {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse peer url %q: %v", serverURL, err)
	}
	return node.NodeInfo{
		ID:      "mac",
		Addr:    parsed.Hostname(),
		APIAddr: ":" + parsed.Port(),
	}
}

func TestListRunsAggregatesPeerRuns(t *testing.T) {
	var gotLocalOnly string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs" {
			http.NotFound(w, r)
			return
		}
		gotLocalOnly = r.URL.Query().Get("local")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"mac-run-1","status":"running"}]`))
	}))
	defer peer.Close()

	backend := &fakeBackend{nodes: []node.NodeInfo{{ID: "local", Local: true}, peerNode(t, peer.URL)}}
	server := NewServer(backend, &fakeProgramBackend{})

	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/runs", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	if gotLocalOnly != "1" {
		t.Fatalf("peer was asked with local=%q, want the local=1 recursion guard", gotLocalOnly)
	}
	var runs []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&runs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := map[string]bool{}
	for _, run := range runs {
		if id, ok := run["id"].(string); ok {
			ids[id] = true
		}
	}
	if !ids["run-1"] || !ids["mac-run-1"] {
		t.Fatalf("aggregated runs = %v, want both local run-1 and peer mac-run-1", ids)
	}
}

func TestListRunsLocalOnlySkipsPeers(t *testing.T) {
	called := false
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"mac-run-1"}]`))
	}))
	defer peer.Close()

	backend := &fakeBackend{nodes: []node.NodeInfo{peerNode(t, peer.URL)}}
	server := NewServer(backend, &fakeProgramBackend{})

	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/runs?local=1", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if called {
		t.Fatal("local=1 request still fanned out to a peer; the guard must answer from the local store only")
	}
	if strings.Contains(res.Body.String(), "mac-run-1") {
		t.Fatalf("local=1 response leaked a peer run: %s", res.Body.String())
	}
}

func TestProxyToPeerWithRunForwardsAndStopsRecursion(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runs/mac-run-1/stop" {
			_, _ = w.Write([]byte("stopped"))
			return
		}
		http.NotFound(w, r)
	}))
	defer peer.Close()

	backend := &fakeBackend{nodes: []node.NodeInfo{peerNode(t, peer.URL)}}
	server := NewServer(backend, &fakeProgramBackend{})

	// A run the peer owns is forwarded and its answer passed through.
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/mac-run-1/stop", nil)
	if !server.proxyToPeerWithRun(res, req, "/api/runs/mac-run-1/stop") {
		t.Fatal("proxyToPeerWithRun returned false for a run the peer owns")
	}
	if res.Body.String() != "stopped" {
		t.Fatalf("proxied body = %q, want %q", res.Body.String(), "stopped")
	}

	// A run no node owns must not proxy a second time: an already-forwarded
	// request carries the marker and is refused rather than bounced onward.
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/runs/ghost/stop", nil)
	req.Header.Set(proxyMarker, "1")
	if server.proxyToPeerWithRun(res, req, "/api/runs/ghost/stop") {
		t.Fatal("an already-proxied request was forwarded again; recursion guard failed")
	}
}
