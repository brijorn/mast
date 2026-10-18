package node

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/auth"
	mastconfig "github.com/brijorn/mast/internal/config"
)

func TestEnsureADBServerSkipsWhenAndroidDisabled(t *testing.T) {
	node, err := NewNode("node-a", ":0", "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node.Close() }()

	calls := captureADBServerCommands(t, func(args []string) error {
		return nil
	})

	if err := node.EnsureADBServerForConfig(context.Background(), mastconfig.Config{AndroidEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("adb calls = %+v, want none", *calls)
	}
}

func TestEnsureADBServerLeavesReachableServerAlone(t *testing.T) {
	node, err := NewNode("node-a", ":0", "", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node.Close() }()

	calls := captureADBServerCommands(t, func(args []string) error {
		return nil
	})

	err = node.EnsureADBServerForConfig(context.Background(), mastconfig.Config{
		AndroidEnabled: true,
		AdvertiseHost:  "10.0.0.4",
		ADBPort:        5038,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"-H 127.0.0.1 -P 5038 devices"}
	if got := *calls; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("adb calls = %+v, want %+v", got, want)
	}
}

// The server Mast starts listens on loopback only; peers reach it through the
// relay, never by its own port. A start that loses the bind to a server another
// adb client started at the same moment still ends with a server answering.
func TestEnsureADBServerStartsLoopbackServerWhenNoneAnswers(t *testing.T) {
	previous := adbServerStartCheckInterval
	adbServerStartCheckInterval = time.Millisecond
	t.Cleanup(func() { adbServerStartCheckInterval = previous })

	for _, tc := range []struct {
		name       string
		startFails bool
		want       []string
	}{
		{"the start succeeds", false, []string{
			"-H 127.0.0.1 -P 5038 devices",
			"-P 5038 kill-server",
			"-P 5038 start-server",
			"-H 127.0.0.1 -P 5038 devices",
		}},
		{"another client's server wins the bind", true, []string{
			"-H 127.0.0.1 -P 5038 devices",
			"-P 5038 kill-server",
			"-P 5038 start-server",
			"-H 127.0.0.1 -P 5038 devices",
			"-H 127.0.0.1 -P 5038 devices",
		}},
	} {
		node, err := NewNode("node-a", ":0", "", true, false, false)
		if err != nil {
			t.Fatal(err)
		}

		checks := 0
		calls := captureADBServerCommands(t, func(args []string) error {
			switch strings.Join(args, " ") {
			case "-H 127.0.0.1 -P 5038 devices":
				checks += 1
				// Down at first; with a lost race, still not up on the first
				// check after the start.
				if checks == 1 || (tc.startFails && checks == 2) {
					return errors.New("connection refused")
				}
			case "-P 5038 start-server":
				if tc.startFails {
					return errors.New("could not install *smartsocket* listener: Address already in use")
				}
			}
			return nil
		})

		err = node.EnsureADBServerForConfig(context.Background(), mastconfig.Config{
			AndroidEnabled: true,
			AdvertiseHost:  "10.0.0.4",
			ADBPort:        5038,
		})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if got := *calls; strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s: adb calls = %+v, want %+v", tc.name, got, tc.want)
		}
		node.adbRelayMu.Lock()
		relay := node.adbRelay
		node.adbRelayMu.Unlock()
		if relay == nil || relay.target != "10.0.0.4:5038" {
			t.Errorf("%s: relay = %+v, want one serving 10.0.0.4:5038", tc.name, relay)
		}
		_ = node.Close()
	}
}

func captureADBServerCommands(t *testing.T, respond func(args []string) error) *[]string {
	t.Helper()
	original := runADBServerCommand
	var calls []string
	runADBServerCommand = func(_ context.Context, args ...string) ([]byte, error) {
		copied := append([]string(nil), args...)
		calls = append(calls, strings.Join(copied, " "))
		if err := respond(copied); err != nil {
			return nil, err
		}
		return []byte("List of devices attached\n"), nil
	}
	t.Cleanup(func() {
		runADBServerCommand = original
	})
	return &calls
}

// A server another Mast started with -a answers on every interface, so it is
// replaced; one on loopback alone is kept.
func TestADBServerExposedOnlyWhenListeningOnEveryInterface(t *testing.T) {
	if !hasNonLoopbackAddress() {
		t.Skip("no non-loopback interface address to probe")
	}
	for _, tc := range []struct {
		listen  string
		exposed bool
	}{
		{"127.0.0.1:0", false},
		{"0.0.0.0:0", true},
	} {
		ln, err := net.Listen("tcp4", tc.listen)
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if got := adbServerExposed("", port); got != tc.exposed {
			t.Errorf("server on %s: exposed = %v, want %v", tc.listen, got, tc.exposed)
		}
		_ = ln.Close()
	}
}

func hasNonLoopbackAddress() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
			return true
		}
	}
	return false
}

func TestADBRelayPipesAdmittedClientsAndDropsTheRest(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("OKAY"))
			_ = conn.Close()
		}
	}()

	var admit atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	relay := &adbRelay{target: "127.0.0.1:0", upstream: upstream.Addr().String(), cancel: cancel}
	relay.wg.Add(1)
	go func() {
		defer relay.wg.Done()
		relay.run(ctx, func(netip.Addr) bool { return admit.Load() })
	}()
	defer relay.close()

	var addr string
	waitFor(t, time.Second, func() bool {
		relay.mu.Lock()
		defer relay.mu.Unlock()
		if relay.listener == nil {
			return false
		}
		addr = relay.listener.Addr().String()
		return true
	})

	read := func() string {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		data, _ := io.ReadAll(conn)
		return string(data)
	}
	if got := read(); got != "" {
		t.Fatalf("refused client read %q from the adb server", got)
	}
	admit.Store(true)
	if got := read(); got != "OKAY" {
		t.Fatalf("admitted client read %q, want the adb server's OKAY", got)
	}
}

// Only this machine and the peers this node trusts may use its relays: with a
// token, an older peer that never proved itself is not one of them.
func TestAdmitsRelayClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		keyed  bool
		client string
		want   bool
	}{
		{"loopback", true, "127.0.0.1", true},
		{"this node's own address", true, "100.98.45.21", true},
		{"an authenticated peer", true, "100.108.95.85", true},
		{"a peer that never proved itself", true, "100.95.129.50", false},
		{"any peer of a node with no token", false, "100.95.129.50", true},
		{"a tailnet device that is no peer", true, "100.70.1.2", false},
		{"a device on the Wi-Fi", false, "192.168.1.20", false},
	} {
		node := &Node{
			AdvertiseHost: "100.98.45.21",
			Peers: map[string]*PeerConn{
				"finn":        {Addr: "100.108.95.85", authenticated: true},
				"aidan-proxy": {Addr: "100.95.129.50"},
			},
		}
		if tc.keyed {
			node.access.Store(auth.New("fleet-token"))
		}
		if got := node.admitsRelayClient(netip.MustParseAddr(tc.client)); got != tc.want {
			t.Errorf("%s: admitted = %v, want %v", tc.name, got, tc.want)
		}
	}
}
