package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brijorn/mast/internal/node"
)

func TestListDevicesReturnsBatteryHealth(t *testing.T) {
	battery := 53
	powerConnected := true
	currentNow := -479
	trend := -1.9
	backend := &fakeBackend{
		devices: []node.DeviceInfo{
			{
				Serial:                     "phone-1",
				State:                      "device",
				NodeID:                     "node-1",
				BatteryPercent:             &battery,
				PowerConnected:             &powerConnected,
				PowerSource:                "usb",
				BatteryStatus:              "charging",
				PowerHealth:                "plugged_draining",
				BatteryCurrentNow:          &currentNow,
				BatteryTrendPercentPerHour: &trend,
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
	if got[0].PowerHealth != "plugged_draining" || got[0].PowerSource != "usb" {
		t.Fatalf("device = %+v, want plugged-draining usb", got[0])
	}
	if got[0].PowerConnected == nil || !*got[0].PowerConnected {
		t.Fatalf("PowerConnected = %v, want true", got[0].PowerConnected)
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

type screenshotBackend struct {
	fakeBackend
	serial string
	png    []byte
}

func (b *screenshotBackend) Screenshot(serial string) ([]byte, error) {
	b.serial = serial
	return b.png, nil
}
