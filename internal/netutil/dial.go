package netutil

import (
	"context"
	"net"
	"net/http"
	"runtime"
	"time"
)

var androidDNSServers = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
}

func HTTPClient() *http.Client {
	if runtime.GOOS != "android" {
		return http.DefaultClient
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = DialContext
	return &http.Client{Transport: transport}
}

func DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	if runtime.GOOS != "android" {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, address)
	}
	return androidDialer().DialContext(ctx, network, address)
}

func androidDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial:     androidResolverDial,
		},
	}
}

func androidResolverDial(ctx context.Context, network string, _ string) (net.Conn, error) {
	var lastErr error
	for _, server := range androidDNSServers {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, network, server)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
