package api

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/brijorn/mast/internal/auth"
	"github.com/brijorn/mast/internal/netutil"
)

// accessBackend is the part of a node that decides whom the API acts for.
type accessBackend interface {
	AccessKeys() *auth.Keys
	AllowedOrigins() []string
	// TrustsPeerAddress reports whether addr is a peer that proved it holds
	// the token.
	TrustsPeerAddress(addr netip.Addr) bool
}

// ticketRoutes are the two sockets a browser phone screen opens. A browser
// cannot set an Authorization header on a websocket, so it presents a ticket
// Runway minted instead, scoped to the device named by the serial parameter.
var ticketRoutes = map[string]bool{
	"/api/streams/video": true,
	"/api/control/ws":    true,
}

// guard decides whether the API acts on a request at all.
//
// A request that carries an Origin header came from a web page, and is served
// only for a page origin the node allows: a page elsewhere must not drive a
// phone through a browser that happens to be on the tailnet.
//
// A node with no token then serves everyone, as nodes did before
// authentication. With one, it serves a process on its own machine -- the
// programs it runs and the tools beside it call loopback with no credential,
// and its loopback adb server already gives such a process every phone -- and,
// on the same terms, a process on a peer that proved it holds the token: a
// coordinator's programs drive this node's phones by calling it directly for
// screenshots and their interaction stream, carrying no credential, and the
// adb relay already admits that machine. Beyond those it serves whoever
// presents the token, and a browser holding a ticket for the device it opens.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, _ := s.node.(accessBackend)
		if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(access, origin) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		var keys *auth.Keys
		if access != nil {
			keys = access.AccessKeys()
		}
		if keys == nil || fromThisMachine(r) || fromTrustedPeer(access, r) ||
			keys.CheckBearer(r.Header.Get("Authorization")) || ticketAdmits(keys, r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="mast"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

func originAllowed(access accessBackend, origin string) bool {
	if access == nil {
		return false
	}
	return slices.Contains(access.AllowedOrigins(), strings.TrimRight(origin, "/"))
}

// fromThisMachine reports whether a request came from a process on this
// machine.
func fromThisMachine(r *http.Request) bool {
	remote, ok := netutil.RemoteAddr(r.RemoteAddr)
	return ok && netutil.IsLoopback(remote) && hostNamesAnAddress(r)
}

// fromTrustedPeer reports whether a request came from a peer that proved it
// holds the token.
func fromTrustedPeer(access accessBackend, r *http.Request) bool {
	remote, ok := netutil.RemoteAddr(r.RemoteAddr)
	return ok && !netutil.IsLoopback(remote) && hostNamesAnAddress(r) && access.TrustsPeerAddress(remote)
}

// hostNamesAnAddress reports whether the Host header names an address rather
// than a domain. A web page that rebinds its own domain to a trusted address
// reaches it from the operator's browser, but still sends its domain as the
// Host.
func hostNamesAnAddress(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	_, err := netip.ParseAddr(host)
	return err == nil
}

func ticketAdmits(keys *auth.Keys, r *http.Request) bool {
	if r.Method != http.MethodGet || !ticketRoutes[r.URL.Path] {
		return false
	}
	query := r.URL.Query()
	ticket := query.Get("ticket")
	if ticket == "" {
		return false
	}
	return keys.CheckTicket(ticket, query.Get("serial"), time.Now()) == nil
}
