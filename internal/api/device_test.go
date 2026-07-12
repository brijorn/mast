package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/node"
)

func TestListDevicesReturnsBatteryHealth(t *testing.T) {
	battery := 53
	backend := &fakeBackend{
		devices: []node.DeviceInfo{
			{
				Serial:   "phone-1",
				Platform: node.PlatformIOS,
				State:    "device",
				NodeID:   "node-1",
				Battery: &node.DeviceBattery{
					Percent: &battery,
					State:   node.BatteryStatePluggedDraining,
				},
			},
		},
	}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)

	server.ListDevices(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}

	var got []node.DeviceInfo
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(response) = %d, want 1", len(got))
	}
	if got[0].Battery == nil || got[0].Battery.State != node.BatteryStatePluggedDraining {
		t.Fatalf("device = %+v, want plugged-draining battery", got[0])
	}
	if got[0].Platform != node.PlatformIOS {
		t.Fatalf("Platform = %q, want ios", got[0].Platform)
	}
}

func TestScreenshotReturnsPNG(t *testing.T) {
	backend := &screenshotBackend{png: []byte("png-bytes")}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/devices/phone-1/screenshot", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if backend.serial != "phone-1" {
		t.Fatalf("serial = %q, want phone-1", backend.serial)
	}
	if got := res.Body.String(); got != "png-bytes" {
		t.Fatalf("body = %q, want png-bytes", got)
	}
}

func TestDeviceGeometryReturnsSeparateCoordinateSpaces(t *testing.T) {
	backend := &geometryTestBackend{geometry: &node.DeviceGeometry{
		Serial:           "ios-1",
		Platform:         node.PlatformIOS,
		Orientation:      "portrait",
		ScreenshotWidth:  1179,
		ScreenshotHeight: 2556,
		InputWidth:       393,
		InputHeight:      852,
	}}
	server := NewServer(backend)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/devices/ios-1/geometry", nil)
	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	var got node.DeviceGeometry
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ScreenshotWidth != 1179 || got.InputWidth != 393 || backend.serial != "ios-1" {
		t.Fatalf("geometry = %+v, requested serial = %q", got, backend.serial)
	}
}

func TestDeviceDNSReturnsStatus(t *testing.T) {
	backend := &fakeBackend{
		dns: &node.DeviceDNSStatus{
			Mode: node.DeviceDNSModeAutomatic,
		},
	}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/devices/phone-1/dns", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.serials[0] != "phone-1" {
		t.Fatalf("serial = %q, want phone-1", backend.serials[0])
	}

	var got node.DeviceDNSStatus
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Mode != node.DeviceDNSModeAutomatic {
		t.Fatalf("dns status = %+v, want automatic", got)
	}
}

func TestSetDeviceDNSReturnsUpdatedStatus(t *testing.T) {
	backend := &fakeBackend{
		dns: &node.DeviceDNSStatus{
			Mode:     node.DeviceDNSModeHostname,
			Hostname: "dns.adguard.com",
		},
	}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/devices/phone-1/dns",
		bytes.NewReader([]byte(`{"mode":"hostname","hostname":"dns.adguard.com"}`)),
	)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.serials[0] != "phone-1" {
		t.Fatalf("serial = %q, want phone-1", backend.serials[0])
	}

	var got node.DeviceDNSStatus
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Mode != node.DeviceDNSModeHostname || got.Hostname != "dns.adguard.com" {
		t.Fatalf("dns status = %+v, want adguard hostname", got)
	}
	if backend.dnsSet == nil || backend.dnsSet.Mode != node.DeviceDNSModeHostname || backend.dnsSet.Hostname != "dns.adguard.com" {
		t.Fatalf("desired dns = %+v, want adguard hostname", backend.dnsSet)
	}
}

func TestGetDeviceBlacklistReturnsConfiguredSerials(t *testing.T) {
	backend := &configNodeBackend{
		config: &mastconfig.Config{DeviceBlacklist: []string{"ios-2", "android-1", "ios-2"}},
	}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/nodes/node-a/device-blacklist", nil)

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.nodeID != "node-a" {
		t.Fatalf("nodeID = %q, want node-a", backend.nodeID)
	}

	var got deviceBlacklistResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := []string{"android-1", "ios-2"}
	if len(got.Serials) != len(want) {
		t.Fatalf("serials = %+v, want %+v", got.Serials, want)
	}
	for i := range want {
		if got.Serials[i] != want[i] {
			t.Fatalf("serials = %+v, want %+v", got.Serials, want)
		}
	}
}

func TestAddDeviceBlacklistPersistsConfigAndReportsRestartRequired(t *testing.T) {
	backend := &configNodeBackend{
		config: &mastconfig.Config{DeviceBlacklist: []string{"android-1"}},
		result: &mastconfig.UpdateResult{
			Config:              mastconfig.Config{DeviceBlacklist: []string{"android-1", "ios-2"}},
			ChangedKeys:         []string{"device_blacklist"},
			RestartRequired:     true,
			RestartRequiredKeys: []string{"device_blacklist"},
		},
	}
	server := NewServer(backend)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/node-a/device-blacklist", bytes.NewReader([]byte(`{"serial":"ios-2"}`)))

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if backend.values["device_blacklist"] != "android-1,ios-2" {
		t.Fatalf("device_blacklist value = %q, want android-1,ios-2", backend.values["device_blacklist"])
	}

	var got deviceBlacklistResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.RestartRequired || len(got.RestartRequiredKeys) != 1 || got.RestartRequiredKeys[0] != "device_blacklist" {
		t.Fatalf("response = %+v, want restart required for device_blacklist", got)
	}
}

type screenshotBackend struct {
	fakeBackend
	serial string
	png    []byte
}

func (b *screenshotBackend) Screenshot(serial string) ([]byte, error) {
	b.serial = serial
	return b.png, nil
}

type geometryTestBackend struct {
	fakeBackend
	serial   string
	geometry *node.DeviceGeometry
}

func (b *geometryTestBackend) Geometry(serial string) (*node.DeviceGeometry, error) {
	b.serial = serial
	return b.geometry, nil
}
