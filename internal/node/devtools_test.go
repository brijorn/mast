package node

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// The relay carries a DevTools session, which is an HTTP request followed by a
// websocket on the same port, so the tests exercise bytes rather than a mock.

func newTestNode(t *testing.T) *Node {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Node{ID: "test-node", ctx: ctx}
}

// A stand-in for the adb forward: something on loopback that answers.
func echoServer(t *testing.T) (int, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, func() {
		_ = listener.Close()
		<-done
	}
}

func TestDevToolsRelayCarriesBytesBothWays(t *testing.T) {
	node := newTestNode(t)
	upstreamPort, stop := echoServer(t)
	defer stop()

	relay, port, err := node.startDevToolsRelay("127.0.0.1", upstreamPort)
	if err != nil {
		t.Fatalf("start relay: %v", err)
	}
	defer relay.close()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GET /json/version HTTP/1.1\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "GET /json/version HTTP/1.1\r\n" {
		t.Fatalf("relayed %q", got)
	}
}

// Chrome refuses a DevTools request whose Host header is a name rather than an
// IP, and reports it in a way that names neither Mast nor the node. A forward
// published on a named host is therefore refused where the cause is still
// visible.
func TestDevToolsRelayRefusesNonIPHost(t *testing.T) {
	node := newTestNode(t)

	_, _, err := node.startDevToolsRelay("finn", 9222)
	if err == nil {
		t.Fatal("a named advertise host was accepted")
	}
	if !strings.Contains(err.Error(), "IP advertise host") {
		t.Fatalf("error does not explain the cause: %v", err)
	}
}

func TestDevToolsRelayCloseStopsListening(t *testing.T) {
	node := newTestNode(t)
	upstreamPort, stop := echoServer(t)
	defer stop()

	relay, port, err := node.startDevToolsRelay("127.0.0.1", upstreamPort)
	if err != nil {
		t.Fatalf("start relay: %v", err)
	}
	relay.close()

	if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		conn.Close()
		t.Fatal("relay still accepts connections after close")
	}
}

// A published forward has to survive a real HTTP round trip, because that is
// what a DevTools client does before it upgrades anything.
func TestDevToolsRelayServesHTTP(t *testing.T) {
	node := newTestNode(t)

	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/devtools/browser/x"}`, r.Host)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = upstream.Serve(listener) }()
	defer upstream.Close()

	relay, port, err := node.startDevToolsRelay("127.0.0.1", listener.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatalf("start relay: %v", err)
	}
	defer relay.close()

	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	res, err := http.Get(endpoint + "/json/version")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	// The client dials the socket URL the browser reports, so it has to name the
	// relay rather than the loopback port behind it.
	want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/x", port)
	if !strings.Contains(string(body), want) {
		t.Fatalf("body %q does not report the relay endpoint %q", body, want)
	}
}

func TestUnpublishUnknownForwardIsNotAnError(t *testing.T) {
	node := newTestNode(t)
	// Tidying up after a failure should not require knowing how far it got.
	if err := node.UnpublishDevToolsForward("nobody", 4321); err != nil {
		t.Fatalf("unpublishing an unknown forward: %v", err)
	}
}
