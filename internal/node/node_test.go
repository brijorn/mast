package node

import (
	"bytes"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

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

func connectNode(t *testing.T, from *Node, to *Node) {
	t.Helper()

	_, port, err := net.SplitHostPort(to.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := from.Connect("ws://127.0.0.1:" + port + "/ws"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		from.mu.RLock()
		defer from.mu.RUnlock()
		_, ok := from.Peers[to.ID]
		return ok
	})
}

func connectPeerToNode(t *testing.T, peer *Node, target *Node) {
	t.Helper()

	_, port, err := net.SplitHostPort(target.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Connect("ws://127.0.0.1:" + port + "/ws"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		target.mu.RLock()
		defer target.mu.RUnlock()
		_, ok := target.Peers[peer.ID]
		return ok
	})
}

func connectNodePair(t *testing.T, nodeA *Node, nodeB *Node) {
	t.Helper()

	connectNode(t, nodeA, nodeB)
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

func TestNodeConnectIsIdempotentForConnectedTarget(t *testing.T) {
	nodeA, nodeB := createNodePair(t)

	_, port, err := net.SplitHostPort(nodeB.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	target := "ws://127.0.0.1:" + port + "/ws"
	if err := nodeA.Connect(target); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		nodeA.mu.RLock()
		defer nodeA.mu.RUnlock()
		_, ok := nodeA.Peers["b"]
		return ok
	})

	nodeA.mu.RLock()
	before := nodeA.Peers["b"]
	nodeA.mu.RUnlock()

	if err := nodeA.Connect(target); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	nodeA.mu.RLock()
	after := nodeA.Peers["b"]
	nodeA.mu.RUnlock()

	if after != before {
		t.Fatal("duplicate Connect replaced the existing peer connection")
	}
}

func TestHasPeerConnectionAllowsDifferentTargetOnSameHost(t *testing.T) {
	node := &Node{Peers: map[string]*PeerConn{
		"first": {Addr: "127.0.0.1", Target: "ws://127.0.0.1:6270/ws"},
	}}

	if node.hasPeerConnection("ws://127.0.0.1:7000/ws", "127.0.0.1") {
		t.Fatal("different port on the same host was treated as an existing peer")
	}
}

func TestNodeDisconnectPeerDropsConnection(t *testing.T) {
	nodeA, nodeB := createNodePair(t)

	_, port, err := net.SplitHostPort(nodeB.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	target := "ws://127.0.0.1:" + port + "/ws"
	if err := nodeA.Connect(target); err != nil {
		t.Fatal(err)
	}

	waitFor(t, time.Second, func() bool {
		nodeA.mu.RLock()
		defer nodeA.mu.RUnlock()
		_, ok := nodeA.Peers["b"]
		return ok
	})

	if !nodeA.DisconnectPeer(target) {
		t.Fatal("DisconnectPeer returned false for connected peer")
	}

	waitFor(t, time.Second, func() bool {
		nodeA.mu.RLock()
		defer nodeA.mu.RUnlock()
		_, ok := nodeA.Peers["b"]
		return !ok
	})
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
	var buf lockedBuffer
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
