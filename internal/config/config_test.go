package config

import (
	"encoding/json"
	"testing"
)

func TestKeepDisplayOffDefaultsOnForExistingConfig(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"android_enabled":true}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.KeepDisplayOff {
		t.Fatal("KeepDisplayOff = false for config without keep_display_off, want true")
	}
}

func TestApplyValuesCanDisableKeepDisplayOffWithoutRestart(t *testing.T) {
	got, changed, restartKeys, err := ApplyValues(Default(), map[string]string{
		"keep_display_off": "false",
	})
	if err != nil {
		t.Fatalf("ApplyValues returned error: %v", err)
	}
	if got.KeepDisplayOff {
		t.Fatal("KeepDisplayOff = true, want false")
	}
	if len(changed) != 1 || changed[0] != "keep_display_off" {
		t.Fatalf("changed = %+v, want [keep_display_off]", changed)
	}
	if len(restartKeys) != 0 {
		t.Fatalf("restartKeys = %+v, want none", restartKeys)
	}
}

// Retuning the encoder is the reason these keys exist, so they have to take
// effect on the next stream rather than on the next restart.
func TestApplyValuesRetunesEncoderWithoutRestart(t *testing.T) {
	got, changed, restartKeys, err := ApplyValues(Default(), map[string]string{
		"stream_video_bitrate":       "4000000",
		"stream_max_size":            "720",
		"stream_video_codec_options": "i-frame-interval=10",
	})
	if err != nil {
		t.Fatalf("ApplyValues returned error: %v", err)
	}
	if got.StreamVideoBitrate != 4_000_000 || got.StreamMaxSize != 720 || got.StreamVideoCodecOptions != "i-frame-interval=10" {
		t.Fatalf("config = %+v, want the requested encoder settings", got)
	}
	if len(changed) != 3 {
		t.Fatalf("changed = %+v, want all three encoder keys", changed)
	}
	if len(restartKeys) != 0 {
		t.Fatalf("restartKeys = %+v, want none", restartKeys)
	}
}

func TestApplyValuesRejectsNegativeBitrate(t *testing.T) {
	if _, _, _, err := ApplyValues(Default(), map[string]string{"stream_video_bitrate": "-1"}); err == nil {
		t.Fatal("ApplyValues returned nil, want error for a negative bitrate")
	}
}

func TestApplyValuesRejectsBatteryProtectionKeys(t *testing.T) {
	_, _, _, err := ApplyValues(Default(), map[string]string{"battery_protection.enabled": "true"})
	if err == nil {
		t.Fatal("ApplyValues returned nil, want error")
	}
}

func TestApplyValuesUpdatesNodeIDAndRequiresRestart(t *testing.T) {
	cfg := Default()

	got, changed, restartKeys, err := ApplyValues(cfg, map[string]string{
		"node_id": " pixel-proxy ",
	})
	if err != nil {
		t.Fatalf("ApplyValues returned error: %v", err)
	}
	if got.NodeID != "pixel-proxy" {
		t.Fatalf("NodeID = %q, want pixel-proxy", got.NodeID)
	}
	if len(changed) != 1 || changed[0] != "node_id" {
		t.Fatalf("changed = %+v, want [node_id]", changed)
	}
	if len(restartKeys) != 1 || restartKeys[0] != "node_id" {
		t.Fatalf("restartKeys = %+v, want [node_id]", restartKeys)
	}
}

func TestApplyValuesUpdatesDeviceBlacklistAndRequiresRestart(t *testing.T) {
	cfg := Default()

	got, changed, restartKeys, err := ApplyValues(cfg, map[string]string{
		"device_blacklist": " ios-2,android-1 ios-2 ",
	})
	if err != nil {
		t.Fatalf("ApplyValues returned error: %v", err)
	}

	want := []string{"android-1", "ios-2"}
	if len(got.DeviceBlacklist) != len(want) {
		t.Fatalf("DeviceBlacklist = %+v, want %+v", got.DeviceBlacklist, want)
	}
	for i := range want {
		if got.DeviceBlacklist[i] != want[i] {
			t.Fatalf("DeviceBlacklist = %+v, want %+v", got.DeviceBlacklist, want)
		}
	}
	if len(changed) != 1 || changed[0] != "device_blacklist" {
		t.Fatalf("changed = %+v, want [device_blacklist]", changed)
	}
	if len(restartKeys) != 1 || restartKeys[0] != "device_blacklist" {
		t.Fatalf("restartKeys = %+v, want [device_blacklist]", restartKeys)
	}
}
