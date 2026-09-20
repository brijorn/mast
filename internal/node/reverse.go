package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"

	"github.com/brijorn/mast/internal/transport"
)

// An adb reverse on the node that owns the handset.
//
// `adb reverse` binds the loopback of the machine running the adb server, so the
// port it names means nothing anywhere else. The coordinator can therefore point
// a phone it owns straight at its own port, but a phone on a peer would be
// pointed at that peer's loopback — which is why this used to be refused for
// anything but a locally owned device.
//
// The owner instead publishes a loopback relay that dials the origin the request
// names, and reverses the phone onto that. The phone still sees the page on
// `localhost`, which is the whole point: the Reco harvester signs in through
// Firebase, and Firebase authorizes `localhost` rather than the coordinator's
// hostname, so serving the page on any other origin fails before the token
// exists.
//
// The relay is a byte pipe rather than an HTTP proxy because nothing here needs
// to read the traffic: rewriting paths or headers would only add a way for the
// page the phone loads to differ from the page the coordinator serves.
type reverseRelay struct {
	listener net.Listener
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func (r *reverseRelay) close() {
	if r == nil {
		return
	}
	r.cancel()
	_ = r.listener.Close()
	r.wg.Wait()
}

// startReverseRelay binds a loopback port that forwards to `origin`, returning
// the port it bound. Port 0 lets the kernel choose, so two phones on one node
// never collide on the same relay.
func (n *Node) startReverseRelay(origin string) (*reverseRelay, int, error) {
	if origin == "" {
		return nil, 0, fmt.Errorf("an origin is required to reverse a peer-owned device")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, fmt.Errorf("publish reverse relay: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(n.ctx)
	relay := &reverseRelay{listener: listener, cancel: cancel}

	relay.wg.Add(1)
	go func() {
		defer relay.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				// A closed listener is how this loop is meant to end.
				return
			}
			relay.wg.Add(1)
			go func() {
				defer relay.wg.Done()
				relay.pipe(ctx, conn, origin)
			}()
		}
	}()

	return relay, port, nil
}

func (r *reverseRelay) pipe(ctx context.Context, client net.Conn, target string) {
	defer client.Close()

	var dialer net.Dialer
	upstream, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		log.Printf("reverse relay: dial %s: %v", target, err)
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

// reverseKey identifies a published reverse. The port is part of it because
// removal names the port it was given, and a phone may be reversed more than
// once across sessions.
func reverseKey(serial string, port int) string {
	return serial + "/" + strconv.Itoa(port)
}

func (n *Node) registerReverseRelay(serial string, port int, relay *reverseRelay) {
	n.reverseMu.Lock()
	defer n.reverseMu.Unlock()
	if n.reverseRelays == nil {
		n.reverseRelays = make(map[string]*reverseRelay)
	}
	key := reverseKey(serial, port)
	// A second reverse on the same port replaces the first rather than leaking
	// the listener the phone is no longer pointed at.
	if existing := n.reverseRelays[key]; existing != nil {
		existing.close()
	}
	n.reverseRelays[key] = relay
}

func (n *Node) releaseReverseRelay(serial string, port int) {
	n.reverseMu.Lock()
	key := reverseKey(serial, port)
	relay := n.reverseRelays[key]
	delete(n.reverseRelays, key)
	n.reverseMu.Unlock()
	relay.close()
}

// reverseLocal points `serial`'s `port` at this machine. With no origin the
// phone reaches this node's own loopback, which is what the coordinator wants
// for a device it owns; with one, the traffic is relayed on to that address.
func (n *Node) reverseLocal(serial string, port int, origin string) error {
	if origin == "" {
		return n.adbReverse(n.ctx, "", serial, "tcp:"+strconv.Itoa(port), port)
	}

	relay, relayPort, err := n.startReverseRelay(origin)
	if err != nil {
		return err
	}
	if err := n.adbReverse(n.ctx, "", serial, "tcp:"+strconv.Itoa(port), relayPort); err != nil {
		relay.close()
		return err
	}
	n.registerReverseRelay(serial, port, relay)
	return nil
}

func (n *Node) removeReverseLocal(serial string, port int) error {
	err := n.adbReverseRemove(n.ctx, "", serial, "tcp:"+strconv.Itoa(port))
	// The relay goes either way: leaving a listener up for a reverse the phone
	// no longer has is a port held open for nothing.
	n.releaseReverseRelay(serial, port)
	return err
}

func (n *Node) peerReverse(ctx context.Context, peerID, serial string, port int, origin string, remove bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeReverseRequest, transport.ReverseRequestPayload{
		Serial: serial,
		Port:   port,
		Origin: origin,
		Remove: remove,
	})
	if err != nil {
		return fmt.Errorf("adb reverse on peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeReverseResponse {
		return fmt.Errorf("unexpected response type: %s", response.messageType)
	}
	var result transport.ReverseResponse
	if err := json.Unmarshal(response.data, &result); err != nil {
		return err
	}
	if result.Payload.Error != "" {
		return fmt.Errorf("adb reverse on peer %s: %s", peerID, result.Payload.Error)
	}
	return nil
}

func (n *Node) handleReverseRequest(peer *PeerConn, req transport.ReverseRequest) {
	var err error
	if req.Payload.Remove {
		err = n.removeReverseLocal(req.Payload.Serial, req.Payload.Port)
	} else {
		err = n.reverseLocal(req.Payload.Serial, req.Payload.Port, req.Payload.Origin)
	}

	payload := transport.ReverseResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	}

	n.writePeerResponse(peer, transport.TypeReverseResponse, req.RawMessage, payload)
}
