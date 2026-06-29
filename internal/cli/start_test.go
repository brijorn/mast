package cli

import "testing"

func TestResolveNodeIDUsesConfigValue(t *testing.T) {
	got, err := resolveNodeID(Config{NodeID: " pixel-proxy "})
	if err != nil {
		t.Fatalf("resolveNodeID returned error: %v", err)
	}
	if got != "pixel-proxy" {
		t.Fatalf("node ID = %q, want pixel-proxy", got)
	}
}

func TestResolveNodeIDFallsBackToHostname(t *testing.T) {
	got, err := resolveNodeID(Config{})
	if err != nil {
		t.Fatalf("resolveNodeID returned error: %v", err)
	}
	if got == "" {
		t.Fatal("node ID is empty")
	}
}
