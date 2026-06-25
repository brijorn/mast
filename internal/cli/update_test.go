package cli

import "testing"

func TestUpdatePathUsesLocalEndpointByDefault(t *testing.T) {
	got := updatePath("")
	if got != "/api/update" {
		t.Fatalf("path = %q", got)
	}
}

func TestUpdatePathUsesNodeEndpoint(t *testing.T) {
	got := updatePath("node-a")
	if got != "/api/nodes/node-a/update" {
		t.Fatalf("path = %q", got)
	}
}

func TestUpdatePathEscapesNodeID(t *testing.T) {
	got := updatePath("node/a")
	if got != "/api/nodes/node%2Fa/update" {
		t.Fatalf("path = %q", got)
	}
}
