package node

import "time"

// deviceOwnerTTL is how long a resolved owner stays good without rechecking.
//
// A device changes hands only when it is physically moved or re-enumerated, so
// this is generous by the standard of what it guards; a stale answer costs one
// failed send, which invalidates the entry and resolves again.
const deviceOwnerTTL = 30 * time.Second

type deviceOwnerEntry struct {
	nodeID   string
	platform string
	at       time.Time
}

// deviceNodeID answers which node owns a serial, reusing a recent answer.
//
// The control paths ask this of every event, and for a peer-owned device the
// full lookup enumerates local adb, misses, then fans a list_devices RPC out to
// every peer and waits for them -- measured at 157ms against 0.25ms for a
// device this node owns, with the whole inventory taking 1.4s. Paying that per
// pointer move capped a drag on a peer phone near six events a second, so the
// websocket queue in front of it overflowed, reported "control queue full", and
// then replayed what it had banked as scrolling nobody asked for any more.
//
// Ownership is the only thing these callers want, and it is exactly the part
// that does not change between one event of a gesture and the next.
func (n *Node) deviceNodeID(serial string) (string, error) {
	nodeID, _, err := n.deviceOwnerInfo(serial)
	return nodeID, err
}

// deviceOwnerInfo answers both parts of the routing decision: which node owns
// the serial, and what platform it is.
func (n *Node) deviceOwnerInfo(serial string) (string, string, error) {
	n.deviceOwnerMu.RLock()
	entry, ok := n.deviceOwners[serial]
	n.deviceOwnerMu.RUnlock()
	if ok && time.Since(entry.at) < deviceOwnerTTL {
		return entry.nodeID, entry.platform, nil
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return "", "", err
	}

	n.rememberDeviceOwner(serial, device.NodeID, device.Platform)
	return device.NodeID, device.Platform, nil
}

func (n *Node) rememberDeviceOwner(serial string, nodeID string, platform string) {
	n.deviceOwnerMu.Lock()
	defer n.deviceOwnerMu.Unlock()
	if n.deviceOwners == nil {
		n.deviceOwners = make(map[string]deviceOwnerEntry)
	}
	n.deviceOwners[serial] = deviceOwnerEntry{nodeID: nodeID, platform: platform, at: time.Now()}
}

// forgetDeviceOwnersOf drops what a peer was believed to own, so a device that
// moved is resolved afresh rather than aimed at a node that has gone.
func (n *Node) forgetDeviceOwnersOf(nodeID string) {
	n.deviceOwnerMu.Lock()
	defer n.deviceOwnerMu.Unlock()
	for serial, entry := range n.deviceOwners {
		if entry.nodeID == nodeID {
			delete(n.deviceOwners, serial)
		}
	}
}

// forgetDeviceOwner drops one serial's cached owner after a send to it failed.
func (n *Node) forgetDeviceOwner(serial string) {
	n.deviceOwnerMu.Lock()
	defer n.deviceOwnerMu.Unlock()
	delete(n.deviceOwners, serial)
}

// sendDeviceInput routes one input operation to the node that owns the device,
// running it here when that is this node.
func (n *Node) sendDeviceInput(serial string, local func() error, messageType string, payload any) error {
	nodeID, err := n.deviceNodeID(serial)
	if err != nil {
		return err
	}

	if nodeID == n.ID {
		return local()
	}

	if err := n.sendPeerRequest(nodeID, messageType, payload); err != nil {
		// The owner we had is no longer reachable for this device; make the
		// next event resolve rather than repeat a send that cannot land.
		n.forgetDeviceOwner(serial)
		return err
	}

	return nil
}
