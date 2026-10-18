package auth

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestNewWithoutATokenHasNoKeys(t *testing.T) {
	if keys := New("  "); keys != nil {
		t.Fatalf("New(blank) = %v, want nil", keys)
	}
}

func TestCheckBearer(t *testing.T) {
	keys := New("fleet-token")
	for header, want := range map[string]bool{
		"Bearer fleet-token":  true,
		keys.Bearer():         true,
		"Bearer fleet-token2": false,
		"Bearer ":             false,
		"fleet-token":         false,
		"Basic fleet-token":   false,
		"":                    false,
	} {
		if got := keys.CheckBearer(header); got != want {
			t.Errorf("header %q: accepted = %v, want %v", header, got, want)
		}
	}
}

func TestCheckTicket(t *testing.T) {
	keys := New("fleet-token")
	now := time.Unix(1_800_000_000, 0)
	valid := keys.MintTicket("R5CT32NENWE", now.Add(time.Minute))
	payload, signature, _ := strings.Cut(valid, ".")
	otherSerial := keys.MintTicket("ZD222X5ZGS", now.Add(time.Minute))
	otherPayload, _, _ := strings.Cut(otherSerial, ".")

	for _, tc := range []struct {
		name   string
		ticket string
		at     time.Time
		want   error
	}{
		{"valid", valid, now, nil},
		{"expired", valid, now.Add(time.Minute), ErrTicketExpired},
		{"another device", otherSerial, now, ErrTicketSerial},
		{"claims swapped under a signature", otherPayload + "." + signature, now, ErrTicketSignature},
		{"another fleet's key", New("other-token").MintTicket("R5CT32NENWE", now.Add(time.Minute)), now, ErrTicketSignature},
		{"outlives the cap", keys.MintTicket("R5CT32NENWE", now.Add(MaxTicketLifetime+time.Minute)), now, ErrTicketLifetime},
		{"no signature", payload, now, ErrTicketMalformed},
		{"empty", "", now, ErrTicketMalformed},
	} {
		if err := keys.CheckTicket(tc.ticket, "R5CT32NENWE", tc.at); !errors.Is(err, tc.want) {
			t.Errorf("%s: CheckTicket = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// A proof verifies only for the role, challenge, node and acceptor address it
// was made for, so it cannot be replayed on another connection, reflected back
// as the other side's, claimed by another node, or relayed through a third
// address.
func TestPeerProofBindsEveryPart(t *testing.T) {
	keys := New("fleet-token")
	acceptor := netip.MustParseAddr("100.95.129.50")
	proof := keys.PeerProof(RoleDialer, "challenge-a", "BMO", acceptor)

	if !keys.CheckPeerProof(proof, RoleDialer, "challenge-a", "BMO", acceptor) {
		t.Fatal("proof does not verify for the values it was made for")
	}
	for _, tc := range []struct {
		name      string
		keys      *Keys
		role      string
		challenge string
		node      string
		acceptor  netip.Addr
	}{
		{"another fleet's key", New("other-token"), RoleDialer, "challenge-a", "BMO", acceptor},
		{"reflected as the acceptor's", keys, RoleAcceptor, "challenge-a", "BMO", acceptor},
		{"another connection's challenge", keys, RoleDialer, "challenge-b", "BMO", acceptor},
		{"claimed by another node", keys, RoleDialer, "challenge-a", "finn", acceptor},
		{"relayed through another address", keys, RoleDialer, "challenge-a", "BMO", netip.MustParseAddr("100.64.0.9")},
	} {
		if tc.keys.CheckPeerProof(proof, tc.role, tc.challenge, tc.node, tc.acceptor) {
			t.Errorf("%s: proof verified, want refused", tc.name)
		}
	}

	overNothing := keys.PeerProof(RoleDialer, "", "BMO", acceptor)
	if keys.CheckPeerProof(overNothing, RoleDialer, "", "BMO", acceptor) {
		t.Error("a proof over no challenge verified; it would be the same on every connection")
	}
}
