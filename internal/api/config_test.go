package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	mastconfig "github.com/brijorn/mast/internal/config"
)

func TestGetNodeConfigReturnsConfig(t *testing.T) {
	backend := &configNodeBackend{config: &mastconfig.Config{AndroidEnabled: true, IOSEnabled: true, ADBPort: 5038}}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/nodes/local-node/config", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.nodeID != "local-node" {
		t.Fatalf("nodeID = %q, want local-node", backend.nodeID)
	}
	var got mastconfig.Config
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.AndroidEnabled || !got.IOSEnabled || got.ADBPort != 5038 {
		t.Fatalf("config = %+v", got)
	}
}

func TestUpdateNodeConfigForwardsValues(t *testing.T) {
	backend := &configNodeBackend{
		result: &mastconfig.UpdateResult{
			Config:              mastconfig.Config{AndroidEnabled: true, IOSEnabled: true, APIAddr: ":7001"},
			ChangedKeys:         []string{"android_enabled", "ios_enabled", "api_addr"},
			RestartRequired:     true,
			RestartRequiredKeys: []string{"api_addr"},
		},
	}
	server := NewServer(backend)

	body := []byte(`{"values":{"android_enabled":true,"ios_enabled":true,"api_addr":":7001","runners":{".py":"python3"}}}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/nodes/remote-node/config", bytes.NewReader(body))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.nodeID != "remote-node" {
		t.Fatalf("nodeID = %q, want remote-node", backend.nodeID)
	}
	if backend.values["android_enabled"] != "true" || backend.values["ios_enabled"] != "true" || backend.values["api_addr"] != ":7001" || backend.values["runners..py"] != "python3" {
		t.Fatalf("values = %+v", backend.values)
	}

	var got mastconfig.UpdateResult
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.RestartRequired || len(got.RestartRequiredKeys) != 1 || got.RestartRequiredKeys[0] != "api_addr" {
		t.Fatalf("result = %+v", got)
	}
}

func TestUpdateNodeConfigRejectsBatteryProtectionObject(t *testing.T) {
	server := NewServer(&configNodeBackend{})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/nodes/local-node/config", bytes.NewReader([]byte(`{"values":{"battery_protection":{"enabled":true}}}`)))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
}

func TestUpdateNodeConfigRejectsInvalidKey(t *testing.T) {
	backend := &configNodeBackend{updateErr: errors.New("invalid config key: wat")}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/nodes/local-node/config", bytes.NewReader([]byte(`{"wat":true}`)))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
}

type configNodeBackend struct {
	fakeBackend
	nodeID    string
	values    map[string]string
	config    *mastconfig.Config
	result    *mastconfig.UpdateResult
	updateErr error
}

func (b *configNodeBackend) GetNodeConfig(_ context.Context, nodeID string) (*mastconfig.Config, error) {
	b.nodeID = nodeID
	return b.config, nil
}

func (b *configNodeBackend) UpdateNodeConfig(_ context.Context, nodeID string, values map[string]string) (*mastconfig.UpdateResult, error) {
	b.nodeID = nodeID
	b.values = values
	return b.result, b.updateErr
}
