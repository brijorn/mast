package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	// Each run is stamped with the node that runs it: the local run with this
	// node's id, the peer's run with the peer's.
	host := map[string]string{}
	for _, run := range runs {
		id, _ := run["id"].(string)
		h, _ := run["host_node_id"].(string)
		host[id] = h
	}
	if host["run-1"] != "local" {
		t.Fatalf("local run host_node_id = %q, want %q", host["run-1"], "local")
	}
	if host["mac-run-1"] != "mac" {
		t.Fatalf("peer run host_node_id = %q, want %q", host["mac-run-1"], "mac")
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

func TestStartRunsForwardsToOwningPeer(t *testing.T) {
	var gotBody string
	var gotMarker string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runs" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		gotMarker = r.Header.Get(proxyMarker)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`[{"id":"peer-run","status":"running"}]`))
	}))
	defer peer.Close()

	backend := &fakeBackend{
		nodes:   []node.NodeInfo{{ID: "local", Local: true}, peerNode(t, peer.URL)},
		devices: []node.DeviceInfo{{Serial: "IOS1", NodeID: "mac"}},
	}
	programs := &fakeProgramBackend{}
	server := NewServer(backend, programs)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		strings.NewReader(`{"program_id":"p","serials":["IOS1"],"run_on_owning_node":true}`))
	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(gotBody, `"IOS1"`) {
		t.Fatalf("peer did not receive the start; got body %q", gotBody)
	}
	if gotMarker != "1" {
		t.Fatalf("forwarded start missing proxy marker (got %q); a peer that re-forwards would loop", gotMarker)
	}
	if programs.started.ProgramID != "" {
		t.Fatalf("owning-peer start was also run locally: %+v", programs.started)
	}
	if !strings.Contains(res.Body.String(), "peer-run") {
		t.Fatalf("gateway did not pass the peer's response through: %s", res.Body.String())
	}
}

func TestStartRunsOnOwningNodeRunsLocallyWhenOwnerIsLocal(t *testing.T) {
	peerHit := false
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		peerHit = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer peer.Close()

	backend := &fakeBackend{
		nodes:   []node.NodeInfo{{ID: "local", Local: true}, peerNode(t, peer.URL)},
		devices: []node.DeviceInfo{{Serial: "LOCAL1", NodeID: "local"}},
	}
	programs := &fakeProgramBackend{}
	server := NewServer(backend, programs)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		strings.NewReader(`{"program_id":"p","serials":["LOCAL1"],"run_on_owning_node":true}`))
	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	if peerHit {
		t.Fatal("a locally-owned device was forwarded to a peer; it should run here")
	}
	if programs.started.ProgramID != "p" {
		t.Fatalf("local start was not invoked: %+v", programs.started)
	}
}

func TestStartRunsAlreadyProxiedDoesNotReforward(t *testing.T) {
	peerHit := false
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		peerHit = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer peer.Close()

	backend := &fakeBackend{
		nodes:   []node.NodeInfo{{ID: "local", Local: true}, peerNode(t, peer.URL)},
		devices: []node.DeviceInfo{{Serial: "IOS1", NodeID: "mac"}},
	}
	programs := &fakeProgramBackend{}
	server := NewServer(backend, programs)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		strings.NewReader(`{"program_id":"p","serials":["IOS1"],"run_on_owning_node":true}`))
	req.Header.Set(proxyMarker, "1")
	server.Handler().ServeHTTP(res, req)

	if peerHit {
		t.Fatal("an already-forwarded start was forwarded again; recursion guard failed")
	}
	if programs.started.ProgramID != "p" {
		t.Fatalf("forwarded start did not execute locally on the owner: %+v", programs.started)
	}
}

func TestBuildProgramRejectsUntrustedCaller(t *testing.T) {
	backend := &fakeBackend{nodes: []node.NodeInfo{{ID: "local", Local: true}}}
	programs := &fakeProgramBackend{}
	server := NewServer(backend, programs)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/programs/build", strings.NewReader(""))
	req.RemoteAddr = "203.0.113.9:5000" // not loopback, not a known node
	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; a stranger must not trigger a build", res.Code)
	}
	if programs.builtSourceCount != 0 {
		t.Fatal("untrusted caller still reached BuildFromSource")
	}
}

func TestBuildProgramBuildsForLoopbackCaller(t *testing.T) {
	backend := &fakeBackend{nodes: []node.NodeInfo{{ID: "local", Local: true}}}
	programs := &fakeProgramBackend{}
	server := NewServer(backend, programs)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("recipe", `{"name":"Demo","slug":"demo","command":"go build","entry":{"command":"./demo"}}`)
	part, _ := mw.CreateFormFile("source", "myrepo/go.mod")
	_, _ = part.Write([]byte("module demo\n"))
	_ = mw.Close()

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/programs/build", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.RemoteAddr = "127.0.0.1:5000"
	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	if programs.builtRecipe.Slug != "demo" || programs.builtSourceCount != 1 {
		t.Fatalf("BuildFromSource got recipe %+v, %d sources", programs.builtRecipe, programs.builtSourceCount)
	}
	if !strings.Contains(res.Body.String(), "sha256-built") {
		t.Fatalf("build response missing program id: %s", res.Body.String())
	}
}

func TestStartRunsBuildsOnPeerThenForwards(t *testing.T) {
	srcRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcRoot, "myrepo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "myrepo", "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAST_SOURCE_ROOT", srcRoot)

	var buildSaw string
	var sawSourcePath bool
	var startSawProgramID string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/programs/build":
			_ = r.ParseMultipartForm(1 << 20)
			buildSaw = r.FormValue("recipe")
			// The source file's full relative path must survive as the field
			// name — a bare "source" key would mean the path was lost.
			if r.MultipartForm != nil {
				_, sawSourcePath = r.MultipartForm.File["myrepo/go.mod"]
			}
			_, _ = w.Write([]byte(`{"id":"peer-built"}`))
		case "/api/runs":
			body, _ := io.ReadAll(r.Body)
			var opts struct {
				ProgramID string `json:"program_id"`
			}
			_ = json.Unmarshal(body, &opts)
			startSawProgramID = opts.ProgramID
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[{"id":"peer-run"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()

	backend := &fakeBackend{
		nodes:   []node.NodeInfo{{ID: "local", Local: true}, peerNode(t, peer.URL)},
		devices: []node.DeviceInfo{{Serial: "IOS1", NodeID: "mac"}},
	}
	server := NewServer(backend, &fakeProgramBackend{})

	start := `{"program_id":"bmo-linux","serials":["IOS1"],"run_on_owning_node":true,` +
		`"build":{"sources":["myrepo"],"workdir":"myrepo","command":"true","artifacts":["bin"],` +
		`"name":"Demo","slug":"demo","entry":{"command":"./bin"}}}`
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(start)))

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(buildSaw, `"slug":"demo"`) {
		t.Fatalf("peer build did not receive the recipe; saw %q", buildSaw)
	}
	if !sawSourcePath {
		t.Fatal("source file did not arrive under its relative path; the path was flattened")
	}
	if startSawProgramID != "peer-built" {
		t.Fatalf("forwarded start used program_id %q, want the peer-built id", startSawProgramID)
	}
}

func TestReadBodyPreservingAllowsProxyReplay(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/api/runs/x/autostart",
		strings.NewReader(`{"autostart_reconnect":true}`))
	body := readBodyPreserving(req)
	if string(body) != `{"autostart_reconnect":true}` {
		t.Fatalf("decoded body = %q", body)
	}
	// The proxy replays r.Body; after a decode it must still hold the bytes,
	// else the owning peer receives an empty body and 400s on EOF.
	replayed, _ := io.ReadAll(req.Body)
	if string(replayed) != `{"autostart_reconnect":true}` {
		t.Fatalf("r.Body after read = %q, want the original body preserved for the proxy", replayed)
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
