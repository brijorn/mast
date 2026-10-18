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
// and its loopback adb server already gives such a process every phone --
// plus whoever presents the token, plus a browser holding a ticket for the
// device it opens.
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
		if keys == nil || fromThisMachine(r) || keys.CheckBearer(r.Header.Get("Authorization")) || ticketAdmits(keys, r) {
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
// machine. The Host header must name an address, not a domain: a web page
// that rebinds its own domain to 127.0.0.1 reaches loopback from the
// operator's browser, but still sends its domain as the Host.
func fromThisMachine(r *http.Request) bool {
	remote, ok := netutil.RemoteAddr(r.RemoteAddr)
	if !ok || !netutil.IsLoopback(remote) {
		return false
	}
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
