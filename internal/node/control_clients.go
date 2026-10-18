package node

import (
	"sync"
	"time"
)

// Control websocket clients of a device's stream session.
//
// A program injecting touches through /api/control/ws is using the session as
// surely as a viewer watching its video, so the idle reaper counts these
// clients as use. The socket is not bound to a session — each message looks up
// whichever session the device has now — but its client chose its coordinates
// from the dimensions of the session it started under. So when a session ends,
// for whatever reason, its device's control clients are closed: each reconnects
// through POST /api/streams and reads the coordinate space of the session it
// gets, rather than carrying stale coordinates into a replacement of a
// different size.

type controlClient struct {
	close func()
}

type deviceControlClients struct {
	clients    map[*controlClient]struct{}
	releasedAt time.Time
}

// AttachControlClient records a control connection for serial. closeClient is
// called when the device's session ends; the returned release must be called
// when the connection closes for any reason, and is safe to call twice.
func (n *Node) AttachControlClient(serial string, closeClient func()) func() {
	client := &controlClient{close: closeClient}
	n.controlClientsMu.Lock()
	if n.controlClients == nil {
		n.controlClients = make(map[string]*deviceControlClients)
	}
	device := n.controlClients[serial]
	if device == nil {
		device = &deviceControlClients{clients: make(map[*controlClient]struct{})}
		n.controlClients[serial] = device
	}
	device.clients[client] = struct{}{}
	n.controlClientsMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			n.controlClientsMu.Lock()
			defer n.controlClientsMu.Unlock()
			device := n.controlClients[serial]
			if device == nil {
				return
			}
			delete(device.clients, client)
			if len(device.clients) == 0 {
				device.releasedAt = time.Now()
			}
		})
	}
}

// controlClientUse reports how many control clients serial has and when the
// last of them left.
func (n *Node) controlClientUse(serial string) (int, time.Time) {
	n.controlClientsMu.Lock()
	defer n.controlClientsMu.Unlock()
	device := n.controlClients[serial]
	if device == nil {
		return 0, time.Time{}
	}
	return len(device.clients), device.releasedAt
}

func (n *Node) closeControlClients(serial string) {
	n.controlClientsMu.Lock()
	var closers []func()
	if device := n.controlClients[serial]; device != nil {
		for client := range device.clients {
			closers = append(closers, client.close)
		}
	}
	n.controlClientsMu.Unlock()
	// Closing makes the connection's handler release itself, which takes the
	// lock, so the closers run outside it.
	for _, closeClient := range closers {
		closeClient()
	}
}
