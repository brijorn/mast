package cli

import (
	"path/filepath"
	"testing"
)

func TestNormalizeHTTPBaseAddsLocalhostForPortOnly(t *testing.T) {
	got, err := normalizeHTTPBase(":6271")
	if err != nil {
		t.Fatalf("normalizeHTTPBase returned error: %v", err)
	}

	if got != "http://127.0.0.1:6271" {
		t.Fatalf("base = %q", got)
	}
}

func TestNormalizeHTTPBaseAddsScheme(t *testing.T) {
	got, err := normalizeHTTPBase("100.64.0.1:6271")
	if err != nil {
		t.Fatalf("normalizeHTTPBase returned error: %v", err)
	}

	if got != "http://100.64.0.1:6271" {
		t.Fatalf("base = %q", got)
	}
}

func TestNormalizeHTTPBaseKeepsHTTPS(t *testing.T) {
	got, err := normalizeHTTPBase("https://mast.example.com/api/")
	if err != nil {
		t.Fatalf("normalizeHTTPBase returned error: %v", err)
	}

	if got != "https://mast.example.com/api" {
		t.Fatalf("base = %q", got)
	}
}

func TestAddSavedPeerAppendsOnce(t *testing.T) {
	store := &PeerStore{}

	if !addSavedPeer(store, "ws://100.64.0.2:6270/ws") {
		t.Fatal("addSavedPeer returned false for a new peer")
	}
	if addSavedPeer(store, "ws://100.64.0.2:6270/ws") {
		t.Fatal("addSavedPeer returned true for a duplicate peer")
	}

	if len(store.Peers) != 1 {
		t.Fatalf("peers = %v, want one saved peer", store.Peers)
	}
}

func TestPeerStorePersistsBesideConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	store := &PeerStore{
		Peers: []string{"ws://a:6270/ws", "ws://b:6270/ws"},
	}

	if err := SavePeerStore(configPath, store); err != nil {
		t.Fatalf("SavePeerStore returned error: %v", err)
	}

	got, err := LoadPeerStore(configPath)
	if err != nil {
		t.Fatalf("LoadPeerStore returned error: %v", err)
	}

	expected := []string{"ws://a:6270/ws", "ws://b:6270/ws"}
	if len(got.Peers) != len(expected) {
		t.Fatalf("peers = %v, want %v", got.Peers, expected)
	}

	for i := range expected {
		if got.Peers[i] != expected[i] {
			t.Fatalf("peers = %v, want %v", got.Peers, expected)
		}
	}
}
