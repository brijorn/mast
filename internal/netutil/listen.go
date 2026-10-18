package netutil

import (
	"errors"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// advertisedRetryInterval is how long a node waits before trying again to bind
// an advertised address that does not exist yet.
var advertisedRetryInterval = 10 * time.Second

// Listen binds a node listener. An addr that names no host -- ":6271", the form
// the defaults and every deployed config use -- binds this machine's loopback
// and the node's advertised address, not every interface, so a node on a
// shared Wi-Fi serves only itself and the tailnet it advertises. An addr that
// names a host is the operator's explicit choice and is bound exactly.
//
// The advertised address may not exist yet. Mast starts at boot, often before
// Tailscale has brought its interface up, and on a phone Tailscale can stay
// down for hours. Loopback is bound at once and the advertised address is
// retried in the background, so a node is never down because its tunnel is.
func Listen(addr string, advertiseHost string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if host != "" {
		return net.Listen("tcp", addr)
	}

	loopback, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return nil, err
	}
	// Port 0 asks for any free port, so every address shares the one the
	// first bind was given.
	port = strconv.Itoa(loopback.Addr().(*net.TCPAddr).Port)
	listener := newMultiListener(loopback)
	if ipv6, err := net.Listen("tcp", net.JoinHostPort("::1", port)); err == nil {
		listener.add(ipv6)
	}

	advertised := AdvertisedListenHost(advertiseHost)
	if advertised == "" {
		return listener, nil
	}
	target := net.JoinHostPort(advertised, port)
	ln, err := net.Listen("tcp", target)
	if err == nil {
		listener.add(ln)
		return listener, nil
	}
	log.Printf("listen %s: %v; serving loopback and retrying every %s", target, err, advertisedRetryInterval)
	go listener.retry(target)
	return listener, nil
}

// AdvertisedListenHost returns the host to bind beside loopback, or "" when
// the advertised address is loopback itself or absent.
func AdvertisedListenHost(advertiseHost string) string {
	host := strings.TrimSpace(advertiseHost)
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimSuffix(host, "/")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return ""
	}
	if addr, err := netip.ParseAddr(host); err == nil && (addr.IsLoopback() || addr.IsUnspecified()) {
		return ""
	}
	return host
}

// multiListener presents several bound addresses as one listener, so a server
// serves, and a node closes, all of them together.
type multiListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	first net.Addr

	mu        sync.Mutex
	listeners []net.Listener
}

func newMultiListener(first net.Listener) *multiListener {
	l := &multiListener{
		conns: make(chan net.Conn),
		done:  make(chan struct{}),
		first: first.Addr(),
	}
	l.add(first)
	return l
}

func (l *multiListener) add(ln net.Listener) {
	l.mu.Lock()
	select {
	case <-l.done:
		l.mu.Unlock()
		_ = ln.Close()
		return
	default:
	}
	l.listeners = append(l.listeners, ln)
	l.mu.Unlock()
	go l.serve(ln)
}

func (l *multiListener) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return
		}
		select {
		case l.conns <- conn:
		case <-l.done:
			_ = conn.Close()
			return
		}
	}
}

func (l *multiListener) retry(target string) {
	for {
		select {
		case <-l.done:
			return
		case <-time.After(advertisedRetryInterval):
		}
		ln, err := net.Listen("tcp", target)
		if err != nil {
			continue
		}
		log.Printf("listening on %s", target)
		l.add(ln)
		return
	}
}

func (l *multiListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *multiListener) Close() error {
	l.once.Do(func() {
		l.mu.Lock()
		close(l.done)
		listeners := l.listeners
		l.mu.Unlock()
		for _, ln := range listeners {
			_ = ln.Close()
		}
	})
	return nil
}

func (l *multiListener) Addr() net.Addr {
	return l.first
}
