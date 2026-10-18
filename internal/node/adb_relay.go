package node

import (
	"context"
	"io"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/brijorn/mast/internal/netutil"
)

// adbRelayRetryInterval is how long the relay waits before trying its address
// again when something else holds it.
var adbRelayRetryInterval = 30 * time.Second

// The adb server has no authentication: whoever reaches its port has a shell on
// every phone it holds. So Mast runs it on loopback only, and the coordinator,
// which drives this node's phones through ADB_SERVER_SOCKET, reaches it through
// this relay instead. The relay listens only on the advertised address and
// admits only this machine and the peers this node trusts, so a device on the
// same Wi-Fi, or anything else on the tailnet, gets nothing.
type adbRelay struct {
	target   string
	upstream string
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	mu       sync.Mutex
	listener net.Listener
}

// ensureADBRelay runs the relay on the advertised address for port, replacing
// one that serves another address, and stops it when there is no address to
// serve.
func (n *Node) ensureADBRelay(advertiseHost string, port int) {
	host := netutil.AdvertisedListenHost(advertiseHost)
	target := ""
	if host != "" {
		target = net.JoinHostPort(host, strconv.Itoa(port))
	}

	n.adbRelayMu.Lock()
	defer n.adbRelayMu.Unlock()
	if n.adbRelay != nil && n.adbRelay.target == target {
		return
	}
	n.adbRelay.close()
	n.adbRelay = nil
	if target == "" {
		return
	}

	ctx, cancel := context.WithCancel(n.ctx)
	relay := &adbRelay{
		target:   target,
		upstream: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		cancel:   cancel,
	}
	n.adbRelay = relay
	relay.wg.Add(1)
	go func() {
		defer relay.wg.Done()
		relay.run(ctx, n.admitsRelayClient)
	}()
}

func (n *Node) stopADBRelay() {
	n.adbRelayMu.Lock()
	defer n.adbRelayMu.Unlock()
	n.adbRelay.close()
	n.adbRelay = nil
}

func (r *adbRelay) close() {
	if r == nil {
		return
	}
	r.cancel()
	r.mu.Lock()
	if r.listener != nil {
		_ = r.listener.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *adbRelay) run(ctx context.Context, admits func(netip.Addr) bool) {
	listener, err := r.listen(ctx)
	if err != nil {
		return
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		client, ok := netutil.AddrOf(conn.RemoteAddr())
		if !ok || !admits(client) {
			_ = conn.Close()
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.pipe(ctx, conn)
		}()
	}
}

// listen binds the relay's address, waiting while something else holds it. A
// Windows node may still have a netsh portproxy forwarding the address to the
// loopback server, which serves the same purpose without the client check, and
// is to be removed once the relay can take its place.
func (r *adbRelay) listen(ctx context.Context) (net.Listener, error) {
	logged := false
	for {
		listener, err := net.Listen("tcp", r.target)
		if err == nil {
			r.mu.Lock()
			if ctx.Err() != nil {
				r.mu.Unlock()
				_ = listener.Close()
				return nil, ctx.Err()
			}
			r.listener = listener
			r.mu.Unlock()
			if logged {
				log.Printf("adb relay listening on %s", r.target)
			}
			return listener, nil
		}
		if !logged {
			log.Printf("adb relay: listen %s: %v; retrying every %s", r.target, err, adbRelayRetryInterval)
			logged = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(adbRelayRetryInterval):
		}
	}
}

func (r *adbRelay) pipe(ctx context.Context, client net.Conn) {
	defer client.Close()
	var dialer net.Dialer
	upstream, err := dialer.DialContext(ctx, "tcp", r.upstream)
	if err != nil {
		log.Printf("adb relay: dial %s: %v", r.upstream, err)
		return
	}
	defer upstream.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// adbServerExposed reports whether the adb port answers on an address of this
// machine other than loopback and the advertised one, which means a server
// started with -a is listening on every interface. Only the advertised address
// answering proves nothing: on finn a portproxy holds exactly that address.
func adbServerExposed(advertiseHost string, port int) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	advertised, _ := netutil.RemoteAddr(netutil.AdvertisedListenHost(advertiseHost))
	for _, addr := range addrs {
		prefix, err := netip.ParsePrefix(addr.String())
		if err != nil {
			continue
		}
		ip := prefix.Addr().Unmap()
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip == advertised {
			continue
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}
