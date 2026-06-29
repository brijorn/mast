package node

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestListNodesIncludesLocalAndPeers(t *testing.T) {
	node := &Node{
		ID:             "local-node",
		AdvertiseHost:  "100.64.0.1",
		AndroidEnabled: true,
		IOSEnabled:     true,
		ProxyEnabled:   true,
		Peers: map[string]*PeerConn{
			"remote-node": {
				Addr:           "100.64.0.2",
				AndroidEnabled: false,
				IOSEnabled:     true,
				ProxyEnabled:   true,
				Version:        "0.2.0",
				Commit:         "abc123",
				BuildDate:      "2026-06-25T17:00:00Z",
			},
		},
	}

	got := node.ListNodes()

	if len(got) != 2 {
		t.Fatalf("len(ListNodes()) = %d, want 2: %+v", len(got), got)
	}

	local := got[0]
	if local.ID != "local-node" || !local.Local || local.Addr != "100.64.0.1" || !local.AndroidEnabled || !local.IOSEnabled || !local.ProxyEnabled {
		t.Fatalf("local node = %+v", local)
	}

	remote := got[1]
	expectedRemote := NodeInfo{
		ID:             "remote-node",
		Addr:           "100.64.0.2",
		Local:          false,
		AndroidEnabled: false,
		IOSEnabled:     true,
		ProxyEnabled:   true,
		Version:        "0.2.0",
		Commit:         "abc123",
		BuildDate:      "2026-06-25T17:00:00Z",
	}
	if diff := cmp.Diff(expectedRemote, remote); diff != "" {
		t.Fatalf("remote node mismatch (-want +got):\n%s", diff)
	}
}
