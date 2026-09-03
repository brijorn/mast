package node

import (
	"context"
	"testing"
	"time"
)

// Resolving the owner used to fan a list_devices RPC out to every peer, and the
// control paths did it per event. The answer has to be reused.
func TestDeviceOwnerInfoServesFromCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n := &Node{ID: "BMO", ctx: ctx}

	n.rememberDeviceOwner("serial-a", "finn", PlatformAndroid)

	nodeID, platform, err := n.deviceOwnerInfo("serial-a")
	if err != nil {
		t.Fatalf("deviceOwnerInfo: %v", err)
	}
	if nodeID != "finn" || platform != PlatformAndroid {
		t.Fatalf("got %q/%q, want finn/android", nodeID, platform)
	}
}

// A cached answer has to expire, or a device that moved is aimed at the node it
// used to be on for as long as the process lives.
func TestDeviceOwnerInfoExpires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n := &Node{ID: "BMO", ctx: ctx}

	n.rememberDeviceOwner("serial-a", "finn", PlatformAndroid)
	n.deviceOwnerMu.Lock()
	entry := n.deviceOwners["serial-a"]
	entry.at = time.Now().Add(-deviceOwnerTTL - time.Second)
	n.deviceOwners["serial-a"] = entry
	n.deviceOwnerMu.Unlock()

	// With no adb and no peers the refresh fails, which is the observable
	// difference between serving the stale entry and re-resolving.
	if _, _, err := n.deviceOwnerInfo("serial-a"); err == nil {
		t.Fatal("expired entry was served from cache")
	}
}

// A peer going away must not leave its devices pointed at it.
func TestForgetDeviceOwnersOfPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n := &Node{ID: "BMO", ctx: ctx}

	n.rememberDeviceOwner("on-finn", "finn", PlatformAndroid)
	n.rememberDeviceOwner("on-laptop", "LAPTOP", PlatformAndroid)
	n.forgetDeviceOwnersOf("finn")

	n.deviceOwnerMu.RLock()
	defer n.deviceOwnerMu.RUnlock()
	if _, ok := n.deviceOwners["on-finn"]; ok {
		t.Fatal("finn's device kept a stale owner")
	}
	if _, ok := n.deviceOwners["on-laptop"]; !ok {
		t.Fatal("dropped an unrelated peer's device")
	}
}

// A local device still routes locally through the shared helper.
func TestSendDeviceInputRunsLocally(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n := &Node{ID: "BMO", ctx: ctx}
	n.rememberDeviceOwner("serial-a", "BMO", PlatformAndroid)

	ran := false
	err := n.sendDeviceInput("serial-a", func() error {
		ran = true
		return nil
	}, "tap_request", nil)
	if err != nil {
		t.Fatalf("sendDeviceInput: %v", err)
	}
	if !ran {
		t.Fatal("local device did not run locally")
	}
}

// A failed send drops the cached owner so the next event resolves afresh
// instead of repeating a send that cannot land.
func TestSendDeviceInputForgetsOwnerAfterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n := &Node{ID: "BMO", ctx: ctx, Peers: map[string]*PeerConn{}}
	n.rememberDeviceOwner("serial-a", "gone", PlatformAndroid)

	if err := n.sendDeviceInput("serial-a", func() error { return nil }, "tap_request", nil); err == nil {
		t.Fatal("send to a missing peer reported success")
	}

	n.deviceOwnerMu.RLock()
	defer n.deviceOwnerMu.RUnlock()
	if _, ok := n.deviceOwners["serial-a"]; ok {
		t.Fatal("owner survived a failed send")
	}
}
