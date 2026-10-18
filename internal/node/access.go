package node

import (
	"encoding/json"
	"log"
	"net/http"
	"net/netip"
	"time"

	"github.com/brijorn/mast/internal/auth"
	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/netutil"
	"github.com/brijorn/mast/internal/transport"
	"github.com/brijorn/mast/internal/version"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// peerResetDelay is how long a node waits, after its token changes, before it
// drops its peers to make them handshake again. The wait lets the response to
// the config update that changed it reach whoever asked.
var peerResetDelay = time.Second

// AccessKeys returns the fleet keys, or nil while the node has no api_token.
// A node without one accepts every caller and every peer, as nodes did before
// authentication existed, so it can still be reached to be given its token.
func (n *Node) AccessKeys() *auth.Keys {
	return n.access.Load()
}

// AllowedOrigins returns the page origins whose browsers may call the API.
func (n *Node) AllowedOrigins() []string {
	origins := n.origins.Load()
	if origins == nil {
		return nil
	}
	return *origins
}

// setAccess adopts cfg's token and allowed origins.
func (n *Node) setAccess(cfg mastconfig.Config) {
	origins := append([]string(nil), cfg.AllowedOrigins...)
	n.origins.Store(&origins)
	keys := auth.New(cfg.APIToken)
	if keys == nil && n.AccessKeys() == nil {
		log.Println("no api_token is configured: the API and peer connections accept anyone who can reach them")
	}
	n.access.Store(keys)
}

// resetPeersForNewToken drops every peer connection so each handshakes again
// under the token now configured. Peers this node dialed are redialed by it;
// the rest redial it themselves.
func (n *Node) resetPeersForNewToken() {
	time.AfterFunc(peerResetDelay, func() {
		n.mu.RLock()
		peers := make([]*PeerConn, 0, len(n.Peers))
		for _, peer := range n.Peers {
			peers = append(peers, peer)
		}
		n.mu.RUnlock()
		for _, peer := range peers {
			_ = peer.conn.Close()
		}
	})
}

// trustsPeer reports whether this node acts on requests a peer sends it. A
// node with no token trusts every peer. With one, only a peer that proved it
// holds the same token is obeyed; an older peer this node dialed stays
// connected, so its devices and proxy keep working, but cannot command this
// node.
func (n *Node) trustsPeer(peer *PeerConn) bool {
	if n.AccessKeys() == nil {
		return true
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	return peer.authenticated
}

// PeerAuthorization returns the Authorization header a request to the node at
// host carries, or "" when it carries none. The token goes only to a peer that
// proved it already holds it: an older peer needs none, and an address that has
// not proved itself must not be handed it.
func (n *Node) PeerAuthorization(host string) string {
	keys := n.AccessKeys()
	if keys == nil {
		return ""
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, peer := range n.Peers {
		if peer.authenticated && peer.Addr == host {
			return keys.Bearer()
		}
	}
	return ""
}

// AuthorizePeerRequest sets the Authorization header on a request bound for a
// peer, when that peer should receive one.
func (n *Node) AuthorizePeerRequest(req *http.Request) {
	if value := n.PeerAuthorization(req.URL.Hostname()); value != "" {
		req.Header.Set("Authorization", value)
	}
}

// TrustsPeerAddress reports whether addr is a peer that proved it holds this
// node's token. Its own advertised address never is: a request reaching the
// node on its own tailnet address from this machine is the tailscale serve hop
// that carries every browser on the tailnet.
func (n *Node) TrustsPeerAddress(addr netip.Addr) bool {
	if n.AccessKeys() == nil {
		return false
	}
	addr = addr.Unmap()
	n.mu.RLock()
	defer n.mu.RUnlock()
	if own, ok := netutil.RemoteAddr(n.AdvertiseHost); ok && own == addr {
		return false
	}
	for _, peer := range n.Peers {
		if peerAddr, ok := netutil.RemoteAddr(peer.Addr); ok && peer.authenticated && peerAddr == addr {
			return true
		}
	}
	return false
}

// admitsRelayClient reports whether a connection from addr may use one of the
// byte relays this node publishes on its advertised address -- the adb server
// and DevTools forwards, neither of which has authentication of its own. Only
// this machine and the peers this node trusts are admitted.
func (n *Node) admitsRelayClient(addr netip.Addr) bool {
	addr = addr.Unmap()
	if netutil.IsLoopback(addr) {
		return true
	}
	keys := n.AccessKeys()
	n.mu.RLock()
	defer n.mu.RUnlock()
	if own, ok := netutil.RemoteAddr(n.AdvertiseHost); ok && own == addr {
		return true
	}
	for _, peer := range n.Peers {
		if keys != nil && !peer.authenticated {
			continue
		}
		if peerAddr, ok := netutil.RemoteAddr(peer.Addr); ok && peerAddr == addr {
			return true
		}
	}
	return false
}

// sendRegistration introduces this node on a peer connection, issuing this
// side's challenge and, when the node has a token and the other side issued a
// challenge, proving itself against it in role.
func (n *Node) sendRegistration(peer *PeerConn, role string, remoteChallenge string) error {
	payload := transport.ConnectionRequestPayload{
		AndroidEnabled: n.AndroidEnabled,
		IOSEnabled:     n.IOSEnabled,
		ProxyEnabled:   n.ProxyEnabled,
		ADBPort:        n.ADBPort,
		APIAddr:        n.APIAddr,
		Version:        version.Version,
		Commit:         version.Commit,
		BuildDate:      version.Date,
		Challenge:      peer.challenge,
	}
	if keys := n.AccessKeys(); keys != nil && remoteChallenge != "" {
		payload.Proof = keys.PeerProof(role, remoteChallenge, n.ID, peer.acceptorAddr)
	}
	data, err := json.Marshal(transport.ConnectionRequest{
		RawMessage: transport.RawMessage{
			Type:      transport.TypeConnectionRequest,
			ID:        uuid.NewString(),
			From:      n.ID,
			Timestamp: time.Now(),
		},
		Payload: payload,
	})
	if err != nil {
		return err
	}
	return peer.WriteMessage(websocket.TextMessage, data)
}

// authenticatePeer decides a peer's registration: whether the peer proved it
// holds this node's token, and whether to keep the connection at all.
//
// A node with no token keeps every peer and vouches for none. With a token, a
// peer that dialed in must prove itself, since anyone on the tailnet can dial.
// A peer this node dialed is at an address the operator saved, so one that
// offers no proof -- an older node, or one not yet given its token -- is kept,
// unauthenticated, so its devices and proxy keep serving while the fleet
// upgrades. One that offers a proof that fails holds some other key, and is
// refused.
func (n *Node) authenticatePeer(peer *PeerConn, dialed bool, nodeID string, payload transport.ConnectionRequestPayload) (authenticated bool, keep bool) {
	keys := n.AccessKeys()
	if keys == nil {
		return false, true
	}
	role := auth.RoleDialer
	if dialed {
		if payload.Proof == "" {
			return false, true
		}
		role = auth.RoleAcceptor
	}
	if keys.CheckPeerProof(payload.Proof, role, peer.challenge, nodeID, peer.acceptorAddr) {
		return true, true
	}
	return false, false
}

// logRefusedPeerRequest notes the first request an untrusted peer sends on a
// connection. An older peer asks for this node's devices on every poll, so a
// line per request would bury the rest of the log.
func (n *Node) logRefusedPeerRequest(peer *PeerConn, peerID string, messageType string) {
	if peer.refusalLogged.Swap(true) {
		return
	}
	log.Printf("ignoring %s and every later request from peer %s on this connection: it has not proved it holds this node's api_token", messageType, peerID)
}
