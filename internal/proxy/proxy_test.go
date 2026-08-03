package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func newTestProxy(t *testing.T) *url.URL {
	t.Helper()
	s := NewServer("", NetworkAuto)
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
