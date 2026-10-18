package proxy

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

// newTestProxy serves a proxy that may dial loopback, so a test can stand its
// upstream up on 127.0.0.1; the guard itself is tested with the real policy.
func newTestProxy(t *testing.T) *url.URL {
	t.Helper()
	s := NewServer("", NetworkAuto)
	s.allowDestination = func(netip.Addr) bool { return true }
	proxy := httptest.NewServer(s.Handler())
	t.Cleanup(proxy.Close)
	proxyUrl, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	return proxyUrl
}
func TestProxyHTTP(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer target.Close()

	proxyUrl := newTestProxy(t)

	client := target.Client()
	client.Transport.(*http.Transport).Proxy = http.ProxyURL(proxyUrl)

	res, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatal(res.Body)
	}

}

func TestProxyHTTPS(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer target.Close()

	proxyUrl := newTestProxy(t)

	client := target.Client()
	client.Transport.(*http.Transport).Proxy = http.ProxyURL(proxyUrl)

	res, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatal(res.Body)
	}

}

// The family has to reach the plain-HTTP path as well as CONNECT, or a
// request that never upgrades leaves by a different address than the tunnels
// beside it — which is the split identity the pinning exists to prevent.
func TestServerPinsBothPathsToTheSameFamily(t *testing.T) {
	s := NewServer(":0", NetworkIPv4)
	if got := s.dialNetwork(); got != NetworkIPv4 {
		t.Fatalf("dialNetwork = %q, want %q", got, NetworkIPv4)
	}
	transport, ok := s.Client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport = %T, want *http.Transport pinned to the family", s.Client.Transport)
	}
	if transport.DialContext == nil {
		t.Fatal("client transport does not pin its dialer, so plain HTTP can leave by another family")
	}
}

func TestServerNetworkIsCorrectableWithoutRebuilding(t *testing.T) {
	s := NewServer(":0", NetworkIPv4)
	s.SetNetwork(NetworkIPv6)
	if got := s.dialNetwork(); got != NetworkIPv6 {
		t.Fatalf("dialNetwork after change = %q, want %q", got, NetworkIPv6)
	}
}

func TestServerRejectsUnknownNetworkRatherThanDialingIt(t *testing.T) {
	s := NewServer(":0", "tcp5")
	if got := s.dialNetwork(); got != NetworkAuto {
		t.Fatalf("dialNetwork = %q, want the auto fallback %q", got, NetworkAuto)
	}
}

// The proxy must not become a way into the node's own loopback, the networks
// around it, or the other tailnet nodes, whether the client names the address
// or a hostname that resolves to it.
func TestProxyRefusesNonPublicDestinations(t *testing.T) {
	proxy := httptest.NewServer(NewServer("", NetworkAuto).Handler())
	t.Cleanup(proxy.Close)
	proxyAddr := proxy.Listener.Addr().String()

	for _, target := range []string{
		"127.0.0.1:6271",
		"localhost:5037",
		"[::1]:6271",
		"192.168.1.54:3000",
		"10.0.0.1:80",
		"100.98.45.21:5037",
		"[fd7a:115c:a1e0::1]:6270",
		"169.254.169.254:80",
		"0.0.0.0:6271",
	} {
		conn, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		_ = conn.Close()
		if err != nil {
			t.Fatalf("CONNECT %s: %v", target, err)
		}
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("CONNECT %s: status %d, want 403", target, res.StatusCode)
		}
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("plain HTTP reached a loopback upstream through the proxy")
	}))
	t.Cleanup(upstream.Close)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxyAddr})}}
	res, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("plain HTTP to loopback: status %d, want 403", res.StatusCode)
	}
}

// A client on the Wi-Fi is refused before the proxy dials anything; this
// machine and the tailnet get as far as the destination check.
func TestProxyServesOnlyThisMachineAndTheTailnet(t *testing.T) {
	handler := NewServer("", NetworkAuto).Handler()
	for _, tc := range []struct {
		remote  string
		refused bool
	}{
		{"192.168.1.20:50000", true},
		{"203.0.113.9:50000", true},
		{"127.0.0.1:50000", false},
		{"100.98.45.21:50000", false},
		{"[fd7a:115c:a1e0::5]:50000", false},
	} {
		req := httptest.NewRequest(http.MethodConnect, "http://127.0.0.1:9/", nil)
		req.Host = "127.0.0.1:9"
		req.RemoteAddr = tc.remote
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		sourceRefused := strings.Contains(rec.Body.String(), "serves only this machine")
		if sourceRefused != tc.refused {
			t.Errorf("client %s: refused as a source = %v, want %v (status %d: %s)", tc.remote, sourceRefused, tc.refused, rec.Code, rec.Body.String())
		}
	}
}
