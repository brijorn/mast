package node

import (
	"bytes"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func createNodePair(t *testing.T) (*Node, *Node) {
	t.Helper()
	nodeA, err := NewNode("a", ":0", "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}

	nodeB, err := NewNode("b", ":0", "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = nodeA.Listen() }()
	go func() { _ = nodeB.Listen() }()

	return nodeA, nodeB
}

func connectNodePair(t *testing.T, nodeA *Node, nodeB *Node) {
	t.Helper()

	_, port, err := net.SplitHostPort(nodeB.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := nodeA.Connect("ws://127.0.0.1:" + port + "/ws"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		nodeA.mu.RLock()
		defer nodeA.mu.RUnlock()
		_, ok := nodeA.Peers["b"]
		return ok
	})
}
func TestNodeConnect(t *testing.T) {

	nodeA, nodeB := createNodePair(t)

	connectNodePair(t, nodeA, nodeB)
}

func TestNodeConnectionStoresPeerVersionMetadata(t *testing.T) {
	nodeA, nodeB := createNodePair(t)

	connectNodePair(t, nodeA, nodeB)

	nodeA.mu.RLock()
	peer := nodeA.Peers["b"]
	nodeA.mu.RUnlock()

	if peer.Version != "dev" {
		t.Fatalf("peer version = %q, want dev", peer.Version)
	}
	if peer.Commit != "unknown" {
		t.Fatalf("peer commit = %q, want unknown", peer.Commit)
	}
	if peer.BuildDate != "unknown" {
		t.Fatalf("peer build date = %q, want unknown", peer.BuildDate)
	}
}

func TestNodeReconect(t *testing.T) {
	nodeA, nodeB := createNodePair(t)

	connectNodePair(t, nodeA, nodeB)

	_, port, err := net.SplitHostPort(nodeB.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	if err := nodeB.Close(); err != nil {
		t.Fatal(err)
	}

	nodeB2, err := NewNode("b", ":"+port, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = nodeB2.Listen() }()

	waitFor(t, 3*time.Second, func() bool {
		nodeA.mu.RLock()
		defer nodeA.mu.RUnlock()
		_, ok := nodeA.Peers["b"]
		return ok
	})
}

func TestNodeClose(t *testing.T) {
	nodeA, nodeB := createNodePair(t)

	connectNodePair(t, nodeA, nodeB)

	// Close initiator to avoid automatic reconnect
	if err := nodeA.Close(); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		nodeB.mu.RLock()
		defer nodeB.mu.RUnlock()
		_, ok := nodeB.Peers["a"]
		return !ok
	})
}

func TestNodeHeartbeat(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	nodeA, nodeB := createNodePair(t)
	nodeA.PingInterval = 100 * time.Millisecond
	connectNodePair(t, nodeA, nodeB)

	time.Sleep(300 * time.Millisecond)

	if !strings.Contains(buf.String(), "sending ping to") {
		t.Fatal("no pings sent")
	}

	if !strings.Contains(buf.String(), "pong received from") {
		t.Fatal("no pongs received")
	}
}
