package config

import "testing"

func TestApplyValuesUpdatesBatteryProtection(t *testing.T) {
	cfg := Default()

	got, changed, restartKeys, err := ApplyValues(cfg, map[string]string{
		"battery_protection.enabled":        "true",
		"battery_protection.min_percent":    "25",
		"battery_protection.resume_percent": "55",
		"battery_protection.stop_program":   "true",
		"battery_protection.stop_stream":    "false",
		"battery_protection.send_home":      "true",
	})
	if err != nil {
		t.Fatalf("ApplyValues returned error: %v", err)
	}
	if !got.BatteryProtection.Enabled || got.BatteryProtection.MinPercent != 25 || got.BatteryProtection.ResumePercent != 55 {
		t.Fatalf("BatteryProtection = %+v, want enabled 25%% and resume at 55%%", got.BatteryProtection)
	}
	if got.BatteryProtection.StopStream {
		t.Fatalf("StopStream = true, want false")
	}
	if len(restartKeys) != 0 {
		t.Fatalf("restartKeys = %+v, want none", restartKeys)
	}
	if len(changed) != 4 {
		t.Fatalf("changed = %+v, want four changed keys", changed)
	}
}

func TestApplyValuesRejectsInvalidBatteryProtectionThreshold(t *testing.T) {
	_, _, _, err := ApplyValues(Default(), map[string]string{"battery_protection.min_percent": "101"})
	if err == nil {
		t.Fatal("ApplyValues returned nil, want error")
	}
}
