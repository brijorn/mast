package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func newTestProxy(t *testing.T) *url.URL {
	t.Helper()
	s := NewServer("")
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
