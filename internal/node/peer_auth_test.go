package node

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/auth"
	mastconfig "github.com/brijorn/mast/internal/config"
)

func keyNode(n *Node, token string) {
	n.setAccess(mastconfig.Config{APIToken: token})
}

func dialTarget(t *testing.T, to *Node) string {
	t.Helper()
	_, port, err := net.SplitHostPort(to.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return "ws://127.0.0.1:" + port + "/ws"
}

func peerAuthenticated(n *Node, id string) (connected bool, authenticated bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	peer, ok := n.Peers[id]
	if !ok {
		return false, false
	}
	return true, peer.authenticated
}

func TestPeersHoldingTheSameTokenAuthenticateEachOther(t *testing.T) {
	dialer, acceptor := createNodePair(t)
	keyNode(dialer, "fleet-token")
	keyNode(acceptor, "fleet-token")

	connectNode(t, dialer, acceptor)
	waitFor(t, time.Second, func() bool {
		_, a := peerAuthenticated(dialer, "b")
		_, b := peerAuthenticated(acceptor, "a")
		return a && b
	})
}

// Anyone on the tailnet can dial a node, so a keyed node keeps no peer that
// dialed in without proving it holds the same token.
func TestKeyedAcceptorRefusesADialerThatCannotProveItself(t *testing.T) {
	for _, tc := range []struct {
		name        string
		dialerToken string
	}{
		{"a dialer with no token", ""},
		{"a dialer holding another token", "other-token"},
	} {
		dialer, acceptor := createNodePair(t)
		keyNode(dialer, tc.dialerToken)
		keyNode(acceptor, "fleet-token")

		if err := dialer.Connect(dialTarget(t, acceptor)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		if connected, _ := peerAuthenticated(acceptor, "a"); connected {
			t.Errorf("%s: the acceptor kept it as a peer", tc.name)
		}
		_ = dialer.Close()
		_ = acceptor.Close()
	}
}

// A keyed node that dials a peer with no token -- an older node, or one not
// yet given its token -- keeps it, so its devices and proxy keep working, but
// ignores what it asks.
func TestKeyedDialerKeepsButIgnoresAPeerWithNoToken(t *testing.T) {
	dialer, acceptor := createNodePair(t)
	keyNode(dialer, "fleet-token")
	dialer.SetConfig(filepath.Join(t.TempDir(), "config.json"), mastconfig.Config{APIToken: "fleet-token", StreamMaxSize: 720}, nil)

	connectNode(t, dialer, acceptor)
	if connected, authenticated := peerAuthenticated(dialer, "b"); !connected || authenticated {
		t.Fatalf("dialer's view of the peer: connected %v, authenticated %v; want connected and unauthenticated", connected, authenticated)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	waitFor(t, time.Second, func() bool { connected, _ := peerAuthenticated(acceptor, "a"); return connected })
	if _, err := acceptor.UpdateNodeConfig(ctx, "a", map[string]string{"stream_max_size": "1080"}); err == nil {
		t.Fatal("the dialer applied a config update from a peer that never proved itself")
	}
	cfg, err := dialer.getLocalConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StreamMaxSize != 720 {
		t.Fatalf("stream_max_size = %d after an untrusted peer's update, want 720", cfg.StreamMaxSize)
	}
}

// The rollout path: a keyed node gives an upgraded peer the token over the
// peer channel, the peer drops its connections, and the redial authenticates
// both sides.
func TestAPeerGivenTheTokenHandshakesAgainAuthenticated(t *testing.T) {
	previous := peerResetDelay
	peerResetDelay = 50 * time.Millisecond
	t.Cleanup(func() { peerResetDelay = previous })

	dialer, acceptor := createNodePair(t)
	keyNode(dialer, "fleet-token")
	acceptor.SetConfig(filepath.Join(t.TempDir(), "config.json"), mastconfig.Config{}, nil)

	connectNode(t, dialer, acceptor)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := dialer.UpdateNodeConfig(ctx, "b", map[string]string{"api_token": "fleet-token"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.APIToken != "" {
		t.Fatal("the config update result carried the token back")
	}
	waitFor(t, 5*time.Second, func() bool {
		_, a := peerAuthenticated(dialer, "b")
		_, b := peerAuthenticated(acceptor, "a")
		return a && b
	})
	if acceptor.PeerAuthorization("127.0.0.1") != auth.New("fleet-token").Bearer() {
		t.Fatal("an authenticated peer's address does not receive the token")
	}
}
