package proxy

import (
	"context"
	"io"
	"maps"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/brijorn/mast/internal/netutil"
)

// The address family the proxy dials out on. A dual-stack cellular link
// answers to both families under different addresses, so leaving the choice to
// happy-eyeballs lets one client's egress address vary between connections —
// which reads to a site as a session moving between hosts, and is what the
// proxy exists to prevent. The family is therefore pinned, not negotiated.
const (
	NetworkAuto = "tcp"
	NetworkIPv4 = "tcp4"
	NetworkIPv6 = "tcp6"
)

type Server struct {
	Addr   string
	Client http.Client

	network atomic.Value
}

func NewServer(addr string, network string) *Server {
	s := &Server{Addr: addr}
	s.SetNetwork(network)
	s.Client = *s.newClient()
	return s
}

// SetNetwork changes the family used by connections opened from now on, so a
// corrected setting reaches traffic without dropping the listener or the
// tunnels already running through it.
func (s *Server) SetNetwork(network string) {
	switch network {
	case NetworkIPv4, NetworkIPv6, NetworkAuto:
	default:
		network = NetworkAuto
	}
	s.network.Store(network)
}

func (s *Server) dialNetwork() string {
	if network, ok := s.network.Load().(string); ok && network != "" {
		return network
	}
	return NetworkAuto
}

// newClient serves the plain-HTTP path, which has to pin the family the same
// way the CONNECT path does: a request that never upgrades would otherwise
// leave by a different address than the tunnels beside it.
func (s *Server) newClient() *http.Client {
	transport, ok := netutil.HTTPClient().Transport.(*http.Transport)
	if !ok {
		transport = http.DefaultTransport.(*http.Transport)
	}
	pinned := transport.Clone()
	pinned.DialContext = func(ctx context.Context, _ string, address string) (net.Conn, error) {
		return netutil.DialContext(ctx, s.dialNetwork(), address)
	}
	return &http.Client{Transport: pinned}
}

func (s *Server) Handler() http.Handler {

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if r.Method == http.MethodConnect {

			targetConn, err := netutil.DialContext(r.Context(), s.dialNetwork(), r.Host)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer func() { _ = targetConn.Close() }()

			hijacker, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijacking not supported", http.StatusInternalServerError)
				return
			}
			clientConn, _, err := hijacker.Hijack()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer func() { _ = clientConn.Close() }()

			if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
				return
			}

			go func() {
				_, _ = io.Copy(targetConn, clientConn)
			}()
			_, _ = io.Copy(clientConn, targetConn)
		} else {
			req, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			req.Header = r.Header

			res, err := s.Client.Do(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer func() { _ = res.Body.Close() }()

			maps.Copy(w.Header(), res.Header)
			w.WriteHeader(res.StatusCode)
			if _, err := io.Copy(w, res.Body); err != nil {
				return
			}

		}

	})
}
func (s *Server) Listen() error {

	return http.ListenAndServe(s.Addr, s.Handler())
}
