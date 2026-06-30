package config

import "testing"

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
