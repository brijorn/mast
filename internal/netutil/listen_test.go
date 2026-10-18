package netutil

import (
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestAdvertisedListenHost(t *testing.T) {
	for advertise, want := range map[string]string{
		"":                    "",
		"127.0.0.1":           "",
		"localhost":           "",
		"0.0.0.0":             "",
		"100.98.45.21":        "100.98.45.21",
		"100.98.45.21:6270":   "100.98.45.21",
		"http://100.95.1.2/":  "100.95.1.2",
		"[fd7a:115c:a1e0::1]": "fd7a:115c:a1e0::1",
	} {
		if got := AdvertisedListenHost(advertise); got != want {
			t.Errorf("advertise %q: binds %q, want %q", advertise, got, want)
		}
	}
}

// A node whose tunnel is not up yet still starts, serving loopback while it
// waits for the advertised address to appear.
func TestListenServesLoopbackWhileTheAdvertisedAddressIsMissing(t *testing.T) {
	ln, err := Listen(":0", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := AddrOf(ln.Addr())
	if !ok || !addr.IsLoopback() {
		t.Fatalf("listener address = %v, want loopback", ln.Addr())
	}

	accepted := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
		}
		accepted <- err
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("accept: %v", err)
	}

	_ = ln.Close()
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("accept after close = %v, want net.ErrClosed", err)
	}
}

func TestAddressClasses(t *testing.T) {
	for _, tc := range []struct {
		addr                      string
		loopback, tailnet, public bool
	}{
		{"127.0.0.1", true, false, false},
		{"127.0.1.1", true, false, false},
		{"::1", true, false, false},
		{"::ffff:127.0.0.1", true, false, false},
		{"100.98.45.21", false, true, false},
		{"fd7a:115c:a1e0::263a:2d15", false, true, false},
		{"100.127.255.255", false, true, false},
		{"192.168.1.54", false, false, false},
		{"10.1.2.3", false, false, false},
		{"172.20.0.1", false, false, false},
		{"169.254.169.254", false, false, false},
		{"fe80::1", false, false, false},
		{"0.0.0.0", false, false, false},
		{"255.255.255.255", false, false, false},
		{"224.0.0.1", false, false, false},
		{"198.18.0.1", false, false, false},
		{"1.1.1.1", false, false, true},
		{"104.21.17.223", false, false, true},
		{"2606:4700:4700::1111", false, false, true},
	} {
		addr := netip.MustParseAddr(tc.addr)
		if got := IsLoopback(addr); got != tc.loopback {
			t.Errorf("%s: loopback = %v, want %v", tc.addr, got, tc.loopback)
		}
		if got := IsTailnet(addr); got != tc.tailnet {
			t.Errorf("%s: tailnet = %v, want %v", tc.addr, got, tc.tailnet)
		}
		if got := IsPublic(addr); got != tc.public {
			t.Errorf("%s: public = %v, want %v", tc.addr, got, tc.public)
		}
	}
}
