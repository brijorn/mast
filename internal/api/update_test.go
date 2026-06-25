package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brijorn/mast/internal/update"
)

func TestCheckUpdateReturnsUpdateResult(t *testing.T) {
	server := NewServer(&fakeBackend{})
	server.updateChecker = &fakeUpdateChecker{
		result: &update.CheckResult{
			CurrentVersion:  "0.1.0",
			LatestVersion:   "0.2.0",
			UpdateAvailable: true,
			OS:              "darwin",
			Arch:            "arm64",
			AssetName:       "mast_0.2.0_darwin_arm64.tar.gz",
			AssetURL:        "https://example.com/mast_0.2.0_darwin_arm64.tar.gz",
			ChecksumURL:     "https://example.com/checksums.txt",
		},
	}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/update", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var got update.CheckResult
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got.CurrentVersion != "0.1.0" || got.LatestVersion != "0.2.0" || !got.UpdateAvailable {
		t.Fatalf("response = %+v", got)
	}
	if got.AssetName != "mast_0.2.0_darwin_arm64.tar.gz" {
		t.Fatalf("AssetName = %q", got.AssetName)
	}
}

func TestCheckUpdateReturnsCheckerError(t *testing.T) {
	server := NewServer(&fakeBackend{})
	server.updateChecker = &fakeUpdateChecker{err: errors.New("github unavailable")}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/update", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
}

func TestApplyUpdateRejectsInvalidJSON(t *testing.T) {
	server := NewServer(&fakeBackend{})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/update", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestCheckNodeUpdateReturnsPeerUpdateResult(t *testing.T) {
	backend := &updateNodeBackend{
		checkResult: &update.CheckResult{
			CurrentVersion:  "0.1.0",
			LatestVersion:   "0.2.0",
			UpdateAvailable: true,
		},
	}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/nodes/remote-node/update", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.nodeID != "remote-node" {
		t.Fatalf("nodeID = %q, want remote-node", backend.nodeID)
	}

	var got update.CheckResult
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.LatestVersion != "0.2.0" {
		t.Fatalf("LatestVersion = %q", got.LatestVersion)
	}
}

func TestApplyNodeUpdateForwardsForce(t *testing.T) {
	backend := &updateNodeBackend{
		applyResult: &update.ApplyResult{
			CurrentVersion:  "0.1.0",
			LatestVersion:   "0.2.0",
			Updated:         true,
			RestartRequired: true,
			Message:         "updated",
		},
	}
	server := NewServer(backend)

	body := []byte(`{"force":true}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/remote-node/update", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.nodeID != "remote-node" {
		t.Fatalf("nodeID = %q, want remote-node", backend.nodeID)
	}
	if !backend.force {
		t.Fatal("Force was not forwarded")
	}
}

type fakeUpdateChecker struct {
	result *update.CheckResult
	err    error
}

func (f *fakeUpdateChecker) Check(_ context.Context) (*update.CheckResult, error) {
	return f.result, f.err
}

type updateNodeBackend struct {
	fakeBackend
	nodeID      string
	force       bool
	checkResult *update.CheckResult
	checkErr    error
	applyResult *update.ApplyResult
	applyErr    error
}

func (b *updateNodeBackend) CheckNodeUpdate(_ context.Context, nodeID string) (*update.CheckResult, error) {
	b.nodeID = nodeID
	return b.checkResult, b.checkErr
}

func (b *updateNodeBackend) ApplyNodeUpdate(_ context.Context, nodeID string, opts update.ApplyOptions) (*update.ApplyResult, error) {
	b.nodeID = nodeID
	b.force = opts.Force
	return b.applyResult, b.applyErr
}
