package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The adb server fails in two ways on finn, and nothing inside adb recovers
// from either. Both begin the same way: adb's own log shows every phone's USB
// transport ending in the same second ("connection terminated: read failed")
// while Windows keeps each phone connected and never announces it again, so
// adb never opens it again either. On 2026-10-13 that happened six times. After
// some of them the server also stopped answering — it kept its listener on 5037,
// every client waited out its deadline, and every client that tried to start a
// replacement failed because the hung one still held the port. After others it
// went on answering `adb devices` with an empty list.
//
// The watchdog answers each the way that worked by hand that day. A server that
// stops answering is killed and replaced. A phone Windows still has connected
// but adb has lost is restarted as a USB device on its own, which makes Windows
// announce it again and the running server open it, without touching any other
// phone. Replacing the whole server instead cut every working phone off too,
// and a burst of replacements left more phones dead each time.
const (
	adbWatchdogInterval = 10 * time.Second
	// Consecutive unanswered probes before the server is replaced. Each probe
	// waits adbCommandTimeout, so a hung server is replaced after roughly a
	// minute, while a server merely slowed by a burst of shell commands answers
	// one of the three.
	adbWatchdogFailureThreshold = 3
	// Consecutive answered probes a phone must be missing from before its device
	// is restarted. One can be a listing caught mid-reconnect.
	adbWatchdogMissingThreshold = 2
	// Device restarts per phone before it is left to an operator, and the wait
	// between them, so a phone that needs a person is not reset forever.
	adbWatchdogMaxDeviceRestarts    = 3
	adbWatchdogDeviceRestartBackoff = 30 * time.Second
	// A remembered phone set older than this describes a rack that may have been
	// rearranged while the node was down, so the watchdog starts from what it
	// sees instead.
	adbWatchdogMemoryMaxAge = time.Hour
	// An unchanged set is saved again this often, so a steady rack is never
	// remembered as older than the age above.
	adbWatchdogMemoryRefresh = 10 * time.Minute
	adbWatchdogFileName      = "adb-watchdog.json"
	// A killed server's port and USB handles take a moment to be released. A
	// replacement that opens the phones before then finds their transports dead.
	adbServerReleaseTimeout = 5 * time.Second
	adbServerSettleDelay    = 2 * time.Second
)

var (
	errADBServerNotFound  = errors.New("no adb server is listening")
	errDeviceNotConnected = errors.New("device is not connected to this machine")
)

type adbWatchdog struct {
	probe   func(context.Context) ([]byte, error)
	replace func(context.Context) error
	// restartDevice restarts one phone's USB device. It is nil where Mast cannot
	// do that, and the watchdog then only replaces a hung server.
	restartDevice func(ctx context.Context, serial string, state string) error
	now           func() time.Time
	stalled       func(bool)

	failures int
	// expected holds the USB phones that have been ready, which are the ones a
	// healthy server should still list.
	expected map[string]struct{}
	missing  map[string]*missingADBPhone
	// path remembers the expected set across restarts of Mast itself: on finn the
	// phones' transports have ended in the seconds around a restart, and a
	// watchdog that learned its phone set after that would see an empty rack as
	// normal.
	path    string
	savedAt time.Time
}

type missingADBPhone struct {
	probes      int
	restarts    int
	lastRestart time.Time
}

type adbWatchdogMemory struct {
	Ready   []string  `json:"ready"`
	SavedAt time.Time `json:"saved_at"`
}

func (n *Node) StartADBWatchdog() {
	n.configMu.RLock()
	configPath := n.configPath
	n.configMu.RUnlock()
	w := &adbWatchdog{
		probe: func(ctx context.Context) ([]byte, error) {
			return n.adb.Devices(ctx, "")
		},
		replace: n.replaceADBServer,
		now:     time.Now,
		stalled: n.adbStalled.Store,
	}
	if restartUSBDevice != nil {
		w.restartDevice = n.restartLostUSBDevice
	}
	if configPath != "" {
		w.path = filepath.Join(filepath.Dir(configPath), adbWatchdogFileName)
		w.load()
	}
	go func() {
		ticker := time.NewTicker(adbWatchdogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-n.ctx.Done():
				return
			case <-ticker.C:
				if n.androidEnabled() {
					w.step(n.ctx)
				}
			}
		}
	}()
}

func (n *Node) androidEnabled() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.AndroidEnabled
}

func (w *adbWatchdog) step(ctx context.Context) {
	output, err := w.probe(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if !adbServerUnresponsive(err) {
			// adb answered with something other than silence, such as a missing
			// binary. Replacing the server cannot fix that.
			w.failures = 0
			w.stalled(false)
			return
		}
		w.failures++
		w.stalled(true)
		if w.failures >= adbWatchdogFailureThreshold {
			log.Printf("replacing the adb server: it has not answered %d probes in a row (last: %v)", w.failures, err)
			if err := w.replace(ctx); err != nil {
				log.Printf("replace adb server: %v", err)
			}
			w.failures = 0
		}
		return
	}
	w.failures = 0
	w.stalled(false)

	states := adbTransportStates(string(output))
	changed := false
	if w.expected == nil {
		w.expected = make(map[string]struct{})
	}
	for transport, state := range states {
		if state != "device" || !isUSBTransport(transport) {
			continue
		}
		if _, ok := w.expected[transport]; !ok {
			w.expected[transport] = struct{}{}
			changed = true
		}
		if phone := w.missing[transport]; phone != nil && phone.restarts > 0 {
			log.Printf("%s is back in adb after %d USB device restart(s)", transport, phone.restarts)
		}
		delete(w.missing, transport)
	}
	if w.restartDevice != nil {
		for transport := range w.expected {
			if states[transport] == "device" {
				continue
			}
			if w.recoverMissing(ctx, transport, states[transport]) {
				delete(w.expected, transport)
				delete(w.missing, transport)
				changed = true
			}
		}
	}
	if changed || w.now().Sub(w.savedAt) >= adbWatchdogMemoryRefresh {
		w.save()
	}
}

// recoverMissing restarts the USB device of an expected phone adb no longer
// lists as ready, and reports whether the phone should stop being expected.
func (w *adbWatchdog) recoverMissing(ctx context.Context, serial string, state string) bool {
	if w.missing == nil {
		w.missing = make(map[string]*missingADBPhone)
	}
	phone := w.missing[serial]
	if phone == nil {
		phone = &missingADBPhone{}
		w.missing[serial] = phone
	}
	phone.probes++
	// An unauthorized phone is waiting for a person to accept the debugging
	// prompt; restarting it would only show them the prompt again.
	if state == "unauthorized" || phone.probes < adbWatchdogMissingThreshold {
		return false
	}
	if phone.restarts >= adbWatchdogMaxDeviceRestarts {
		log.Printf("%s is still missing from adb after %d USB device restarts; leaving it to an operator", serial, phone.restarts)
		return true
	}
	if !phone.lastRestart.IsZero() && w.now().Sub(phone.lastRestart) < adbWatchdogDeviceRestartBackoff {
		return false
	}
	phone.restarts++
	phone.lastRestart = w.now()
	err := w.restartDevice(ctx, serial, state)
	switch {
	case errors.Is(err, errDeviceNotConnected):
		log.Printf("%s is missing from adb and no longer connected to this machine; no longer expecting it", serial)
		return true
	case err != nil:
		log.Printf("restart USB device for %s (attempt %d of %d): %v", serial, phone.restarts, adbWatchdogMaxDeviceRestarts, err)
	default:
		log.Printf("restarted the USB device for %s, which Windows still had connected but adb had lost (attempt %d of %d)", serial, phone.restarts, adbWatchdogMaxDeviceRestarts)
	}
	return false
}

func (w *adbWatchdog) load() {
	data, err := os.ReadFile(w.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("read adb watchdog memory: %v", err)
		}
		return
	}
	var memory adbWatchdogMemory
	if err := json.Unmarshal(data, &memory); err != nil {
		log.Printf("parse adb watchdog memory: %v", err)
		return
	}
	if w.now().Sub(memory.SavedAt) > adbWatchdogMemoryMaxAge {
		return
	}
	w.savedAt = memory.SavedAt
	w.expected = make(map[string]struct{}, len(memory.Ready))
	for _, transport := range memory.Ready {
		if isUSBTransport(transport) {
			w.expected[transport] = struct{}{}
		}
	}
}

func (w *adbWatchdog) save() {
	if w.path == "" {
		return
	}
	memory := adbWatchdogMemory{Ready: make([]string, 0, len(w.expected)), SavedAt: w.now().UTC()}
	for transport := range w.expected {
		memory.Ready = append(memory.Ready, transport)
	}
	slices.Sort(memory.Ready)
	data, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		log.Printf("encode adb watchdog memory: %v", err)
		return
	}
	temp := w.path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		log.Printf("write adb watchdog memory: %v", err)
		return
	}
	if err := os.Rename(temp, w.path); err != nil {
		log.Printf("replace adb watchdog memory: %v", err)
		_ = os.Remove(temp)
		return
	}
	w.savedAt = w.now()
}

// adbServerUnresponsive separates a server that is not answering from adb
// failing for some other reason. A client that waited out its deadline, or one
// that found the port taken but got no greeting from whatever holds it, both
// mean the server itself is the problem.
func adbServerUnresponsive(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := err.Error()
	for _, symptom := range []string{
		"cannot connect to daemon",
		"failed to start daemon",
		"could not read ok from ADB Server",
		"protocol fault",
	} {
		if strings.Contains(message, symptom) {
			return true
		}
	}
	return false
}

func adbTransportStates(output string) map[string]string {
	states := make(map[string]string)
	for _, device := range parseDevicesOutput(output, "", nil) {
		states[device.Serial] = device.State
	}
	return states
}

// isUSBTransport tells a cable from the other transports adb lists. A wireless
// or emulator transport coming and going is a network matter, and has no USB
// device to restart.
func isUSBTransport(transport string) bool {
	return transport != "" && !strings.Contains(transport, ":") && !strings.HasPrefix(transport, "emulator-")
}

// restartLostUSBDevice makes Windows announce a phone again. A transport adb
// still holds as offline is let go first; Windows refuses to restart a device
// a process has open and asks for a reboot instead.
func (n *Node) restartLostUSBDevice(ctx context.Context, serial string, state string) error {
	if state == "offline" {
		if _, err := runADBServerCommand(ctx, "reconnect", "offline"); err != nil {
			log.Printf("let go of offline transports before restarting %s: %v", serial, err)
		}
	}
	return restartUSBDevice(ctx, serial)
}

// replaceADBServer kills the process listening on the adb port and starts one
// replacement once the old one's port and USB handles are released. It does
// not go through EnsureADBServer: that would run kill-server against whatever
// a client had already started meanwhile, and on 2026-10-13 that second kill is
// what left half of finn's phones dead after the watchdog's first replacement.
func (n *Node) replaceADBServer(ctx context.Context) error {
	n.mu.RLock()
	port := n.ADBPort
	n.mu.RUnlock()
	if port <= 0 {
		port = 5037
	}

	pids, err := findADBServerProcesses(port)
	if err != nil && !errors.Is(err, errADBServerNotFound) {
		return fmt.Errorf("find adb server on port %d: %w", port, err)
	}
	for _, pid := range pids {
		process, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("find adb server process %d: %w", pid, err)
		}
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill adb server process %d: %w", pid, err)
		}
		log.Printf("killed adb server process %d", pid)
	}

	waitForPortRelease(ctx, port, adbServerReleaseTimeout)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(adbServerSettleDelay):
	}
	return startADBServer(ctx, port)
}

// startADBServer starts a server peers can reach, as startup does, and falls
// back to a loopback-only one where the node's own address is already
// forwarded to loopback — finn's netsh portproxy holds that address, so a
// server on every interface cannot bind there, and the forward serves the
// loopback one just as well. If a client already started a server meanwhile,
// start-server finds it and leaves it alone.
func startADBServer(ctx context.Context, port int) error {
	portArg := strconv.Itoa(port)
	if _, err := runADBServerCommand(ctx, "-a", "-P", portArg, "start-server"); err == nil {
		return nil
	}
	if _, err := runADBServerCommand(ctx, "-P", portArg, "start-server"); err != nil {
		return fmt.Errorf("start adb server on port %d: %w", port, err)
	}
	return nil
}

func waitForPortRelease(ctx context.Context, port int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for time.Now().Before(deadline) && ctx.Err() == nil {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(200 * time.Millisecond)
	}
}
