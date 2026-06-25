package peer

import "testing"

func TestNormalizeTargetAddsSchemePortAndPath(t *testing.T) {
	got, err := NormalizeTarget("100.64.0.2")
	if err != nil {
		t.Fatalf("NormalizeTarget returned error: %v", err)
	}

	if got != "ws://100.64.0.2:6270/ws" {
		t.Fatalf("target = %q", got)
	}
}

func TestNormalizeTargetKeepsExplicitPort(t *testing.T) {
	got, err := NormalizeTarget("mast-node.local:7000")
	if err != nil {
		t.Fatalf("NormalizeTarget returned error: %v", err)
	}

	if got != "ws://mast-node.local:7000/ws" {
		t.Fatalf("target = %q", got)
	}
}

func TestNormalizeTargetKeepsFullWebsocketURL(t *testing.T) {
	got, err := NormalizeTarget("wss://mast.example.com:7443/custom")
	if err != nil {
		t.Fatalf("NormalizeTarget returned error: %v", err)
	}

	if got != "wss://mast.example.com:7443/custom" {
		t.Fatalf("target = %q", got)
	}
}

func TestNormalizeTargetRejectsHTTP(t *testing.T) {
	_, err := NormalizeTarget("http://mast.example.com:6270/ws")
	if err == nil {
		t.Fatal("NormalizeTarget returned nil error, want invalid scheme")
	}
}
