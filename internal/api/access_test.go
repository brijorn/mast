package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/auth"
)

type keyedBackend struct {
	*fakeBackend
	keys    *auth.Keys
	origins []string
}

func (b keyedBackend) AccessKeys() *auth.Keys   { return b.keys }
func (b keyedBackend) AllowedOrigins() []string { return b.origins }

// With a token, the API acts for its own machine, the token, and a ticket on
// a phone screen's sockets -- and a page origin it does not allow is refused
// whoever sends it.
func TestGuard(t *testing.T) {
	keys := auth.New("fleet-token")
	ticket := keys.MintTicket("R5CT32NENWE", time.Now().Add(time.Minute))
	const runway = "https://runway.brijorn.com"

	for _, tc := range []struct {
		name    string
		keys    *auth.Keys
		remote  string
		host    string
		target  string
		header  http.Header
		refused int
	}{
		{name: "a node with no token serves the Wi-Fi", remote: "192.168.1.20:5000", target: "/api/nodes"},
		{name: "loopback", keys: keys, remote: "127.0.0.1:5000", target: "/api/nodes"},
		{name: "loopback named localhost", keys: keys, remote: "[::1]:5000", host: "localhost:6271", target: "/api/nodes"},
		{name: "loopback reached by a rebound domain", keys: keys, remote: "127.0.0.1:5000", host: "attacker.example:6271", target: "/api/nodes", refused: http.StatusUnauthorized},
		{name: "the tailnet without a credential", keys: keys, remote: "100.70.1.2:5000", target: "/api/nodes", refused: http.StatusUnauthorized},
		{name: "the token", keys: keys, remote: "100.70.1.2:5000", target: "/api/nodes", header: http.Header{"Authorization": {"Bearer fleet-token"}}},
		{name: "another token", keys: keys, remote: "100.70.1.2:5000", target: "/api/nodes", header: http.Header{"Authorization": {"Bearer other"}}, refused: http.StatusUnauthorized},
		{name: "a ticket for the device's video", keys: keys, remote: "100.98.45.21:5000", target: "/api/streams/video?serial=R5CT32NENWE&viewer=v&ticket=" + ticket, header: http.Header{"Origin": {runway}}},
		{name: "a ticket for the device's control", keys: keys, remote: "100.98.45.21:5000", target: "/api/control/ws?serial=R5CT32NENWE&ticket=" + ticket, header: http.Header{"Origin": {runway}}},
		{name: "a ticket for another device", keys: keys, remote: "100.98.45.21:5000", target: "/api/streams/video?serial=ZD222X5ZGS&viewer=v&ticket=" + ticket, refused: http.StatusUnauthorized},
		{name: "a ticket off the socket routes", keys: keys, remote: "100.98.45.21:5000", target: "/api/nodes?serial=R5CT32NENWE&ticket=" + ticket, refused: http.StatusUnauthorized},
		{name: "another page's origin, even from loopback", keys: keys, remote: "127.0.0.1:5000", target: "/api/nodes", header: http.Header{"Origin": {"https://attacker.example"}}, refused: http.StatusForbidden},
		{name: "another page's origin on a node with no token", remote: "100.70.1.2:5000", target: "/api/nodes", header: http.Header{"Origin": {"https://attacker.example"}}, refused: http.StatusForbidden},
	} {
		backend := keyedBackend{fakeBackend: &fakeBackend{}, keys: tc.keys, origins: []string{runway}}
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		req.RemoteAddr = tc.remote
		if tc.host != "" {
			req.Host = tc.host
		} else {
			req.Host = "127.0.0.1:6271"
		}
		for key, values := range tc.header {
			req.Header[key] = values
		}
		rec := httptest.NewRecorder()
		NewServer(backend).Handler().ServeHTTP(rec, req)

		refused := rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden
		switch {
		case tc.refused != 0 && rec.Code != tc.refused:
			t.Errorf("%s: status %d, want %d", tc.name, rec.Code, tc.refused)
		case tc.refused == 0 && refused:
			t.Errorf("%s: refused with %d: %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}
