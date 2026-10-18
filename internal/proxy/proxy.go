package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"syscall"

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

// The proxy exists to give one person's accounts an egress address that is not
// bmo's. It is not a way into the networks around the node: a client on the
// tailnet could otherwise CONNECT to the node's own loopback -- its adb server,
// its API -- or to the Wi-Fi and the other nodes behind it. So it serves only
// this machine and the tailnet, and dials only the public internet. The check
// runs on the address actually dialed, after name resolution, so a hostname
// that resolves somewhere private is refused just like the bare address.
type Server struct {
	Addr   string
	Client http.Client

	network atomic.Value

	allowSource      func(netip.Addr) bool
	allowDestination func(netip.Addr) bool
}

var errDestinationRefused = errors.New("the proxy only dials public internet addresses")

func NewServer(addr string, network string) *Server {
	s := &Server{
		Addr:             addr,
		allowSource:      func(a netip.Addr) bool { return netutil.IsLoopback(a) || netutil.IsTailnet(a) },
		allowDestination: netutil.IsPublic,
	}
	s.SetNetwork(network)
	s.Client = *s.newClient()
	return s
}

// dial opens an upstream connection, refusing any address the proxy must not
// reach.
func (s *Server) dial(ctx context.Context, address string) (net.Conn, error) {
	dialer := netutil.Dialer()
	dialer.Control = func(_ string, address string, _ syscall.RawConn) error {
		addr, ok := netutil.RemoteAddr(address)
		if !ok || !s.allowDestination(addr) {
			return fmt.Errorf("%w: %s", errDestinationRefused, address)
		}
		return nil
	}
	return dialer.DialContext(ctx, s.dialNetwork(), address)
}

func upstreamError(w http.ResponseWriter, err error) {
	if errors.Is(err, errDestinationRefused) {
		http.Error(w, errDestinationRefused.Error(), http.StatusForbidden)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
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
		return s.dial(ctx, address)
	}
	return &http.Client{Transport: pinned}
}

func (s *Server) Handler() http.Handler {

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if source, ok := netutil.RemoteAddr(r.RemoteAddr); !ok || !s.allowSource(source) {
			http.Error(w, "the proxy serves only this machine and the tailnet", http.StatusForbidden)
			return
		}

		if r.Method == http.MethodConnect {

			targetConn, err := s.dial(r.Context(), r.Host)
			if err != nil {
				upstreamError(w, err)
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
				upstreamError(w, err)
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
