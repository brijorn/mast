package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"

	"github.com/brijorn/mast/internal/transport"
)

// Reaching a phone's Chrome DevTools socket from a node that does not own the
// phone.
//
// `adb forward` binds loopback on the machine running the adb server, so the
// port it reports means nothing anywhere else — which is why a peer-owned
// device used to be refused outright. The owner therefore publishes the forward
// on the address peers already reach it by, exactly as a stream start does, and
// answers with a host and port instead of a bare port.
//
// The relay is a byte pipe rather than an HTTP proxy on purpose. A DevTools
// client fetches `/json/version` over HTTP and then upgrades a websocket at
// whatever path that document names, so anything that rewrote paths would have
// to rewrite the protocol too.
//
// One consequence is not negotiable, and it is why the address is validated
// rather than assumed: Chrome refuses a DevTools request whose `Host` header is
// a name rather than an IP or `localhost` ("Host header is specified and is not
// an IP address or localhost"), and it echoes that header back as the
// `webSocketDebuggerUrl` the client then dials. So an advertise host that is a
// DNS name produces a forward that resolves, connects, and is then refused by
// the browser with an error naming neither Mast nor the node.

type devToolsRelay struct {
	listener net.Listener
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func (r *devToolsRelay) close() {
	if r == nil {
		return
	}
	r.cancel()
	_ = r.listener.Close()
	r.wg.Wait()
}

// startDevToolsRelay publishes a loopback port on `host`, returning the port it
// bound. Port 0 lets the kernel choose, so two phones on one node never collide.
func (n *Node) startDevToolsRelay(host string, loopbackPort int) (*devToolsRelay, int, error) {
	if net.ParseIP(host) == nil {
		return nil, 0, fmt.Errorf(
			"devtools forward needs an IP advertise host, but this node advertises %q: chrome refuses a devtools request whose Host header is a name",
			host,
		)
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, 0, fmt.Errorf("publish devtools forward on %s: %w", host, err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(n.ctx)
	relay := &devToolsRelay{listener: listener, cancel: cancel}

	relay.wg.Add(1)
	go func() {
		defer relay.wg.Done()
		target := net.JoinHostPort("127.0.0.1", strconv.Itoa(loopbackPort))
		for {
			conn, err := listener.Accept()
			if err != nil {
				// A closed listener is how this loop is meant to end.
				return
			}
			relay.wg.Add(1)
			go func() {
				defer relay.wg.Done()
				relay.pipe(ctx, conn, target)
			}()
		}
	}()

	return relay, port, nil
}

func (r *devToolsRelay) pipe(ctx context.Context, client net.Conn, target string) {
	defer client.Close()

	var dialer net.Dialer
	upstream, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		log.Printf("devtools relay: dial %s: %v", target, err)
		return
	}
	defer upstream.Close()

	// A DevTools session is one websocket held open for the length of the
	// session, so both directions run until either end hangs up.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()

	select {
	case <-done:
	case <-ctx.Done():
	}
}

// devToolsKey identifies a published forward. The port is part of it because
// removal names the port it was given, and a phone may be forwarded more than
// once across sessions.
func devToolsKey(serial string, port int) string {
	return serial + "/" + strconv.Itoa(port)
}

func (n *Node) registerDevToolsRelay(serial string, port int, relay *devToolsRelay) {
	n.devToolsMu.Lock()
	defer n.devToolsMu.Unlock()
	if n.devToolsRelays == nil {
		n.devToolsRelays = make(map[string]*devToolsRelay)
	}
	n.devToolsRelays[devToolsKey(serial, port)] = relay
}

func (n *Node) releaseDevToolsRelay(serial string, port int) *devToolsRelay {
	n.devToolsMu.Lock()
	defer n.devToolsMu.Unlock()
	key := devToolsKey(serial, port)
	relay := n.devToolsRelays[key]
	delete(n.devToolsRelays, key)
	return relay
}

// PublishDevToolsForward creates the adb forward and publishes it for peers,
// returning the address a peer should dial. Local callers do not use this: a
// forward on the machine that will connect to it needs no relay, and adding one
// would put a second hop under every claim that works today.
func (n *Node) PublishDevToolsForward(serial string) (string, int, error) {
	host := n.AdvertiseHost
	if host == "" {
		return "", 0, errors.New("devtools forward is unavailable: this node has no advertise host configured")
	}

	loopbackPort, err := n.DevToolsForward(serial)
	if err != nil {
		return "", 0, err
	}

	relay, port, err := n.startDevToolsRelay(host, loopbackPort)
	if err != nil {
		_ = n.DevToolsForwardRemove(serial, loopbackPort)
		return "", 0, err
	}

	n.registerDevToolsRelay(serial, port, relay)
	n.trackDevToolsLoopback(serial, port, loopbackPort)
	return host, port, nil
}

// UnpublishDevToolsForward closes a published forward and the adb forward under
// it. Removing one that is already gone is not an error: a caller tidying up
// after a failure should not have to know how far it got.
func (n *Node) UnpublishDevToolsForward(serial string, port int) error {
	relay := n.releaseDevToolsRelay(serial, port)
	if relay == nil {
		return nil
	}
	relay.close()

	loopbackPort := n.releaseDevToolsLoopback(serial, port)
	if loopbackPort == 0 {
		return nil
	}
	return n.DevToolsForwardRemove(serial, loopbackPort)
}

func (n *Node) trackDevToolsLoopback(serial string, port int, loopbackPort int) {
	n.devToolsMu.Lock()
	defer n.devToolsMu.Unlock()
	if n.devToolsLoopback == nil {
		n.devToolsLoopback = make(map[string]int)
	}
	n.devToolsLoopback[devToolsKey(serial, port)] = loopbackPort
}

func (n *Node) releaseDevToolsLoopback(serial string, port int) int {
	n.devToolsMu.Lock()
	defer n.devToolsMu.Unlock()
	key := devToolsKey(serial, port)
	loopbackPort := n.devToolsLoopback[key]
	delete(n.devToolsLoopback, key)
	return loopbackPort
}

// DevToolsEndpoint answers where a DevTools client should connect for a phone,
// wherever that phone is. A locally owned device keeps the loopback forward it
// has always used; a peer-owned one is published by its owner.
func (n *Node) DevToolsEndpoint(serial string) (string, int, error) {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return "", 0, err
	}

	if device.NodeID == n.ID {
		port, err := n.DevToolsForward(serial)
		if err != nil {
			return "", 0, err
		}
		return "127.0.0.1", port, nil
	}

	return n.requestPeerDevToolsForward(n.ctx, device.NodeID, serial)
}

// RemoveDevToolsEndpoint tears down whatever DevToolsEndpoint set up.
func (n *Node) RemoveDevToolsEndpoint(serial string, port int) error {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID == n.ID {
		return n.DevToolsForwardRemove(serial, port)
	}

	return n.requestPeerDevToolsRemove(n.ctx, device.NodeID, serial, port)
}

func (n *Node) requestPeerDevToolsForward(ctx context.Context, nodeID string, serial string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, peerStreamRPCTimeout)
	defer cancel()

	payload := transport.DevToolsForwardRequestPayload{Serial: serial}
	response, err := n.sendPeerRPC(ctx, nodeID, transport.TypeDevToolsForwardRequest, payload)
	if err != nil {
		return "", 0, err
	}
	if response.messageType != transport.TypeDevToolsForwardResponse {
		return "", 0, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DevToolsForwardResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return "", 0, err
	}
	if res.Payload.Error != "" {
		return "", 0, errors.New(res.Payload.Error)
	}
	if res.Payload.Result == nil {
		return "", 0, errors.New("devtools forward response missing result")
	}

	return res.Payload.Result.Host, res.Payload.Result.Port, nil
}

func (n *Node) requestPeerDevToolsRemove(ctx context.Context, nodeID string, serial string, port int) error {
	ctx, cancel := context.WithTimeout(ctx, peerStreamRPCTimeout)
	defer cancel()

	payload := transport.DevToolsRemoveRequestPayload{Serial: serial, Port: port}
	response, err := n.sendPeerRPC(ctx, nodeID, transport.TypeDevToolsRemoveRequest, payload)
	if err != nil {
		return err
	}
	if response.messageType != transport.TypeDevToolsRemoveResponse {
		return fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DevToolsRemoveResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return err
	}
	if res.Payload.Error != "" {
		return errors.New(res.Payload.Error)
	}
	return nil
}

func (n *Node) handleDevToolsForwardRequest(peer *PeerConn, req transport.DevToolsForwardRequest) {
	payload := transport.DevToolsForwardResponsePayload{}
	host, port, err := n.PublishDevToolsForward(req.Payload.Serial)
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = &transport.DevToolsForwardResultPayload{
			Serial: req.Payload.Serial,
			Host:   host,
			Port:   port,
		}
	}

	n.writePeerResponse(peer, transport.TypeDevToolsForwardResponse, req.RawMessage, payload)
}

func (n *Node) handleDevToolsRemoveRequest(peer *PeerConn, req transport.DevToolsRemoveRequest) {
	payload := transport.DevToolsRemoveResponsePayload{}
	if err := n.UnpublishDevToolsForward(req.Payload.Serial, req.Payload.Port); err != nil {
		payload.Error = err.Error()
	}

	n.writePeerResponse(peer, transport.TypeDevToolsRemoveResponse, req.RawMessage, payload)
}
