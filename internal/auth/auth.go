// Package auth holds what a node checks before it acts for anyone other than a
// process on its own machine. Every node in a fleet shares one api_token. The
// token is sent only as a bearer credential; peer proofs and viewer tickets use
// subkeys derived from it for those purposes alone, so neither one can stand in
// for the token or for the other.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"
)

const (
	peerKeyLabel   = "mast peer handshake v1"
	ticketKeyLabel = "mast viewer ticket v1"

	// MaxTicketLifetime bounds how long a ticket may claim to stay valid. A
	// ticket only has to last until its socket opens, so a longer claim is a
	// minting mistake to refuse, not a convenience to honour.
	MaxTicketLifetime = 15 * time.Minute
)

// Peer handshake roles. A proof names the side that made it, so the dialer's
// proof can never be replayed back to it as the acceptor's.
const (
	RoleDialer   = "dialer"
	RoleAcceptor = "acceptor"
)

var (
	ErrTicketMalformed = errors.New("malformed ticket")
	ErrTicketSignature = errors.New("ticket signature does not verify")
	ErrTicketExpired   = errors.New("ticket expired")
	ErrTicketLifetime  = errors.New("ticket outlives the longest lifetime a node accepts")
	ErrTicketSerial    = errors.New("ticket is for another device")
)

// Keys holds a fleet token and the subkeys derived from it. A nil *Keys means
// the node has no token, which is how a node that predates authentication, or
// one just upgraded and not yet given its token, keeps serving its peers.
type Keys struct {
	tokenDigest [sha256.Size]byte
	bearer      string
	peer        []byte
	ticket      []byte
}

// New returns the keys for token, or nil when token is blank.
func New(token string) *Keys {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return &Keys{
		tokenDigest: sha256.Sum256([]byte(token)),
		bearer:      "Bearer " + token,
		peer:        derive(token, peerKeyLabel),
		ticket:      derive(token, ticketKeyLabel),
	}
}

func derive(token string, label string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// Bearer is the Authorization header value that carries the token.
func (k *Keys) Bearer() string {
	return k.bearer
}

// CheckBearer reports whether an Authorization header carries the token. Both
// sides are hashed first so the comparison takes the same time whatever the
// header's length.
func (k *Keys) CheckBearer(header string) bool {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return subtle.ConstantTimeCompare(digest[:], k.tokenDigest[:]) == 1
}

// NewChallenge returns a fresh random value for a peer to prove itself against.
func NewChallenge() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("auth: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// PeerProof proves that the node calling itself nodeID, playing role, holds
// the fleet key, for the challenge the other side issued on this connection.
//
// The proof is bound to the acceptor's address: the dialer uses the address it
// connected to, the acceptor the address it was reached on. A node on the
// tailnet that relays a handshake between two real peers is therefore caught,
// because the address each real peer sees is the relay's, not its partner's.
func (k *Keys) PeerProof(role string, challenge string, nodeID string, acceptor netip.Addr) string {
	mac := hmac.New(sha256.New, k.peer)
	for _, part := range []string{role, challenge, nodeID, acceptor.Unmap().String()} {
		mac.Write([]byte(part))
		mac.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CheckPeerProof reports whether proof is the one PeerProof makes for these
// values. An empty challenge never verifies: it means the challenge was never
// issued, and a proof over nothing would be the same on every connection.
func (k *Keys) CheckPeerProof(proof string, role string, challenge string, nodeID string, acceptor netip.Addr) bool {
	if proof == "" || challenge == "" {
		return false
	}
	want := k.PeerProof(role, challenge, nodeID, acceptor)
	return hmac.Equal([]byte(proof), []byte(want))
}

type ticketClaims struct {
	Serial  string `json:"serial"`
	Expires int64  `json:"exp"`
}

// MintTicket returns a ticket that opens serial's sockets until expires.
// Runway mints the tickets browsers use; this exists so the format has one
// definition on this side, and for tests.
func (k *Keys) MintTicket(serial string, expires time.Time) string {
	claims, _ := json.Marshal(ticketClaims{Serial: serial, Expires: expires.Unix()})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	return payload + "." + k.ticketSignature(payload)
}

func (k *Keys) ticketSignature(payload string) string {
	mac := hmac.New(sha256.New, k.ticket)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CheckTicket verifies a ticket for serial at now.
func (k *Keys) CheckTicket(ticket string, serial string, now time.Time) error {
	payload, signature, ok := strings.Cut(ticket, ".")
	if !ok || payload == "" || signature == "" {
		return ErrTicketMalformed
	}
	if !hmac.Equal([]byte(signature), []byte(k.ticketSignature(payload))) {
		return ErrTicketSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return ErrTicketMalformed
	}
	var claims ticketClaims
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Expires == 0 {
		return ErrTicketMalformed
	}
	expires := time.Unix(claims.Expires, 0)
	if !now.Before(expires) {
		return ErrTicketExpired
	}
	if expires.Sub(now) > MaxTicketLifetime {
		return ErrTicketLifetime
	}
	if claims.Serial != serial {
		return ErrTicketSerial
	}
	return nil
}
