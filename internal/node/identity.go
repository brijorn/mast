package node

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A wireless adb transport names a route, not a device. Android picks a fresh
// ephemeral port every time adbd restarts, and DHCP can move the address, so
// "192.168.1.159:43497" identifies the same phone as "192.168.1.160:34891"
// might tomorrow. Runway keys durable state (people, offers, schedules,
// calibration, run history) on DeviceInfo.Serial, so that field has to hold
// something the phone carries with it: the hardware serial from ro.serialno.
// Address keeps the transport, which is what adb -s actually dials.
//
// USB transports are already the hardware serial, so they resolve to
// themselves and cost no round trip.

const deviceIdentityFileName = "device-identity.json"

// Re-probe a cached address this often. Between refreshes the cache answers,
// which keeps the polled device list from paying an adb round trip per phone.
// The TTL is what lets a recycled address (a new phone landing on the port a
// previous one used) correct itself instead of impersonating its predecessor.
const deviceIdentityTTL = 5 * time.Minute

// Bounded well below adbCommandTimeout: a healthy phone answers getprop in
// well under a second, and the device list must not hang on one that never
// will.
const deviceIdentityProbeTimeout = 3 * time.Second

type deviceIdentityEntry struct {
	Serial     string    `json:"serial"`
	ResolvedAt time.Time `json:"resolved_at"`
}

type deviceIdentityFile struct {
	Addresses map[string]deviceIdentityEntry `json:"addresses"`
}

// isTransportAddress reports whether an adb transport name is a network route
// rather than a hardware serial. Hardware serials never contain a colon.
func isTransportAddress(transport string) bool {
	return strings.Contains(transport, ":")
}

// isNodeLocalTransport reports whether a transport is reachable only from the
// node running it — emulators and loopback-connected devices. Their identity
// is deliberately left as the transport: an emulator's ro.serialno is a
// synthetic value that can repeat across machines, so promoting it to a
// fleet-wide identity would merge two different emulators into one device.
func isNodeLocalTransport(transport string) bool {
	if strings.HasPrefix(transport, "emulator-") {
		return true
	}
	host, _, found := strings.Cut(transport, ":")
	if !found {
		return false
	}
	switch strings.TrimSuffix(strings.TrimPrefix(host, "["), "]") {
	case "127.0.0.1", "localhost", "::1", "":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

func (n *Node) setDeviceIdentityPath(configPath string) {
	if configPath == "" {
		return
	}
	path := filepath.Join(filepath.Dir(configPath), deviceIdentityFileName)

	n.identityMu.Lock()
	if n.identityPath == path && n.identityLoaded {
		n.identityMu.Unlock()
		return
	}
	n.identityPath = path
	n.identityLoaded = true
	n.identityMu.Unlock()

	n.loadDeviceIdentities(path)
}

func (n *Node) loadDeviceIdentities(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("read device identities: %v", err)
		}
		return
	}

	var file deviceIdentityFile
	if err := json.Unmarshal(data, &file); err != nil {
		log.Printf("parse device identities: %v", err)
		return
	}

	n.identityMu.Lock()
	defer n.identityMu.Unlock()
	if n.identityCache == nil {
		n.identityCache = make(map[string]deviceIdentityEntry, len(file.Addresses))
	}
	for address, entry := range file.Addresses {
		if address == "" || entry.Serial == "" {
			continue
		}
		n.identityCache[address] = entry
	}
}

func (n *Node) saveDeviceIdentities() {
	// Identities resolve concurrently, and every saver writes the same temp
	// path before renaming it into place; one at a time keeps a half-written
	// file from being the one that gets renamed.
	n.identitySaveMu.Lock()
	defer n.identitySaveMu.Unlock()

	n.identityMu.RLock()
	path := n.identityPath
	file := deviceIdentityFile{Addresses: make(map[string]deviceIdentityEntry, len(n.identityCache))}
	for address, entry := range n.identityCache {
		file.Addresses[address] = entry
	}
	n.identityMu.RUnlock()

	if path == "" {
		return
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		log.Printf("encode device identities: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("create device identity dir: %v", err)
		return
	}
	// Write and rename so a torn write cannot leave the node without the
	// mapping it needs to identify an unreachable phone.
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		log.Printf("write device identities: %v", err)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		log.Printf("replace device identities: %v", err)
		_ = os.Remove(temp)
	}
}

func (n *Node) cachedDeviceIdentity(address string) (deviceIdentityEntry, bool) {
	n.identityMu.RLock()
	defer n.identityMu.RUnlock()
	entry, ok := n.identityCache[address]
	return entry, ok
}

func (n *Node) cacheDeviceIdentity(address string, serial string) {
	if address == "" || serial == "" {
		return
	}

	n.identityMu.Lock()
	if n.identityCache == nil {
		n.identityCache = make(map[string]deviceIdentityEntry)
	}
	existing, ok := n.identityCache[address]
	unchanged := ok && existing.Serial == serial
	n.identityCache[address] = deviceIdentityEntry{Serial: serial, ResolvedAt: time.Now()}
	n.identityMu.Unlock()

	if unchanged {
		// Only the timestamp moved; the file already says the right thing.
		return
	}
	n.saveDeviceIdentities()
}

// probeDeviceSerial reads the hardware serial off a connected Android device.
//
// The device list is polled, and a transport can be listed as "device" while
// answering nothing at all — a phone reached over a VPN that has gone away
// still shows up in adb devices. The probe is therefore bounded well below the
// normal adb timeout so one dead transport cannot stall the whole listing, and
// retried once so a merely slow phone is not mistaken for a dead one.
func (n *Node) probeDeviceSerial(host string, address string) (string, error) {
	ctx := n.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var err error
	for attempt := range 2 {
		var output []byte
		attemptCtx, cancel := context.WithTimeout(ctx, deviceIdentityProbeTimeout)
		output, err = n.adb.Shell(attemptCtx, host, address, "getprop", "ro.serialno")
		cancel()
		if err == nil {
			return sanitizeDeviceSerial(string(output)), nil
		}
		if attempt == 0 && ctx.Err() == nil {
			continue
		}
		break
	}
	return "", err
}

// sanitizeDeviceSerial rejects the values getprop returns when it has nothing
// useful to say, so an unreadable property never becomes a device identity.
func sanitizeDeviceSerial(raw string) string {
	serial := strings.TrimSpace(strings.ReplaceAll(raw, "\r", ""))
	if index := strings.IndexAny(serial, "\n"); index != -1 {
		serial = strings.TrimSpace(serial[:index])
	}
	switch strings.ToLower(serial) {
	case "", "unknown", "null":
		return ""
	}
	if isTransportAddress(serial) {
		return ""
	}
	return serial
}

// resolveDeviceIdentity fills in Address and replaces a transport-shaped
// Serial with the device's hardware serial. It reports false when a wireless
// device's identity cannot be established, which keeps the device out of the
// listing entirely: reporting it under its address would let Runway write
// durable rows keyed on a string that expires at the next reconnect.
func (n *Node) resolveDeviceIdentity(device *DeviceInfo, host string) bool {
	device.Address = device.Serial

	if device.Platform != PlatformAndroid {
		return true
	}
	// A USB transport is already the hardware serial, and a node-local
	// transport deliberately keeps its address as identity.
	if !isTransportAddress(device.Serial) || isNodeLocalTransport(device.Serial) {
		return true
	}

	address := device.Address
	cached, hasCache := n.cachedDeviceIdentity(address)
	if hasCache && time.Since(cached.ResolvedAt) < deviceIdentityTTL {
		device.Serial = cached.Serial
		return true
	}

	// An offline or unauthorized device cannot answer getprop. Its last known
	// identity is exactly what keeps it on its own tile instead of vanishing.
	if device.State != "device" {
		if hasCache {
			device.Serial = cached.Serial
			return true
		}
		log.Printf("skip unidentified device %s: state %s and no cached serial", address, device.State)
		return false
	}

	serial, err := n.probeDeviceSerial(host, address)
	if err != nil || serial == "" {
		if hasCache {
			device.Serial = cached.Serial
			return true
		}
		if err != nil {
			log.Printf("resolve serial for %s: %v", address, err)
		} else {
			log.Printf("resolve serial for %s: device reported no serial", address)
		}
		return false
	}

	device.Serial = serial
	n.cacheDeviceIdentity(address, serial)
	return true
}

// Identities are probed concurrently because a dead transport answers nothing
// until its probe times out, and in series every later phone waits behind it —
// the bound on a single probe only limits the whole listing if the probes
// overlap. The keep decisions are collected first so the surviving devices are
// still compacted in the listed order.
func (n *Node) resolveDeviceIdentities(devices []DeviceInfo, host string) []DeviceInfo {
	keep := make([]bool, len(devices))
	var wg sync.WaitGroup
	for i := range devices {
		wg.Add(1)
		go func(device *DeviceInfo, keep *bool) {
			defer wg.Done()
			*keep = n.resolveDeviceIdentity(device, host)
		}(&devices[i], &keep[i])
	}
	wg.Wait()

	resolved := devices[:0]
	for i := range devices {
		if keep[i] {
			resolved = append(resolved, devices[i])
		}
	}
	return resolved
}

// rememberDeviceAddresses records how each listed device is currently
// reachable, including devices owned by peers, so a control call naming a
// hardware serial can be dialled without re-listing.
func (n *Node) rememberDeviceAddresses(devices []DeviceInfo) {
	if len(devices) == 0 {
		return
	}

	n.addressMu.Lock()
	defer n.addressMu.Unlock()
	if n.addressBySerial == nil {
		n.addressBySerial = make(map[string]string, len(devices))
	}
	for _, device := range devices {
		if device.Serial == "" || device.Address == "" {
			continue
		}
		n.addressBySerial[device.Serial] = device.Address
	}
}

// deviceAddress maps a device identity to the transport adb should dial.
// An unknown serial returns itself, which is correct for every USB device and
// is the safe answer for anything not yet listed.
func (n *Node) deviceAddress(serial string) string {
	if serial == "" {
		return serial
	}

	n.addressMu.RLock()
	address, ok := n.addressBySerial[serial]
	n.addressMu.RUnlock()
	if ok && address != "" {
		return address
	}
	return serial
}

// The adb wrappers below are the single choke point where a device identity
// becomes a transport. Call sites pass the serial Runway knows; adb receives
// the address that currently reaches the phone.

func (n *Node) adbShell(ctx context.Context, host string, serial string, arg ...string) ([]byte, error) {
	return n.adb.Shell(ctx, host, n.deviceAddress(serial), arg...)
}

func (n *Node) adbExecOut(ctx context.Context, host string, serial string, arg ...string) ([]byte, error) {
	return n.adb.ExecOut(ctx, host, n.deviceAddress(serial), arg...)
}

func (n *Node) adbPush(ctx context.Context, host string, serial string, localPath string, remotePath string) error {
	return n.adb.Push(ctx, host, n.deviceAddress(serial), localPath, remotePath)
}

func (n *Node) adbReverse(ctx context.Context, host string, serial string, deviceSocket string, localPort int) error {
	return n.adb.Reverse(ctx, host, n.deviceAddress(serial), deviceSocket, localPort)
}

func (n *Node) adbForward(ctx context.Context, host string, serial string, localSpec string, deviceSocket string) ([]byte, error) {
	return n.adb.Forward(ctx, host, n.deviceAddress(serial), localSpec, deviceSocket)
}

func (n *Node) adbForwardRemove(ctx context.Context, host string, serial string, localSpec string) error {
	return n.adb.ForwardRemove(ctx, host, n.deviceAddress(serial), localSpec)
}

func (n *Node) adbStartShell(host string, serial string, arg ...string) (*exec.Cmd, error) {
	return n.adb.StartShell(host, n.deviceAddress(serial), arg...)
}
