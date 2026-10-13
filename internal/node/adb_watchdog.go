package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// The adb server can stop answering while staying alive: on a Windows node it
// keeps its listener on 5037 but never replies, so every client waits out its
// deadline, and every new client that tries to start a replacement fails
// because the hung one still holds the port. Nothing inside adb recovers from
// that, and on 2026-10-13 a node's phones sat unreachable for hours at a time
// until someone killed the process by hand. The watchdog does what that person
// did: it notices the silence, kills the process listening on the adb port, and
// lets a fresh server take its place.
const (
	adbWatchdogInterval = 10 * time.Second
	// Consecutive unanswered probes before the server is replaced. Each probe
	// waits adbCommandTimeout, so a hung server is replaced after roughly a
	// minute, while a server merely slowed by a burst of shell commands answers
	// one of the three.
	adbWatchdogFailureThreshold = 3
	// How long a replacement server is given to enumerate the phones again before
	// its result is judged.
	adbWatchdogSettleDelay = 20 * time.Second
	// Replacing the server can leave USB transports the old one held stuck
	// offline; a second replacement once the stack has settled has brought them
	// back where the first did not. Beyond this many the phones are not coming
	// back through adb, and further restarts would only cut off the ones that did.
	adbWatchdogMaxFollowUps = 2
)

var errADBServerNotFound = errors.New("no adb server is listening")

type adbWatchdog struct {
	probe   func(context.Context) ([]byte, error)
	replace func(context.Context) error
	now     func() time.Time
	stalled func(bool)

	failures int
	// expected holds the transports that were ready at the last healthy probe,
	// which is what a replacement server is expected to find again.
	expected    map[string]struct{}
	restartedAt time.Time
	followUps   int
}

func (n *Node) StartADBWatchdog() {
	w := &adbWatchdog{
		probe: func(ctx context.Context) ([]byte, error) {
			return n.adb.Devices(ctx, "")
		},
		replace: n.replaceADBServer,
		now:     time.Now,
		stalled: n.adbStalled.Store,
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
			w.restart(ctx, fmt.Sprintf("it has not answered %d probes in a row (last: %v)", w.failures, err))
		}
		return
	}

	w.failures = 0
	w.stalled(false)
	ready := readyADBTransports(string(output))
	if w.restartedAt.IsZero() {
		w.expected = ready
		return
	}

	returned := 0
	for transport := range w.expected {
		if _, ok := ready[transport]; ok {
			returned++
		}
	}
	// Half is the line between a replacement that lost the phones and one that
	// found them while some other phone happened to be unplugged meanwhile; only
	// the first is worth cutting off every phone that did come back.
	if returned*2 >= len(w.expected) {
		log.Printf("adb server replaced; %d of %d phones are back", returned, len(w.expected))
		w.endIncident(ready)
		return
	}
	if w.now().Sub(w.restartedAt) < adbWatchdogSettleDelay {
		return
	}
	if w.followUps >= adbWatchdogMaxFollowUps {
		log.Printf("adb server replaced %d times but only %d of %d phones came back; leaving it to an operator", w.followUps+1, returned, len(w.expected))
		w.endIncident(ready)
		return
	}
	w.followUps++
	w.restart(ctx, fmt.Sprintf("only %d of %d phones came back after replacing it", returned, len(w.expected)))
}

func (w *adbWatchdog) restart(ctx context.Context, reason string) {
	log.Printf("replacing the adb server: %s", reason)
	if err := w.replace(ctx); err != nil {
		log.Printf("replace adb server: %v", err)
	}
	w.failures = 0
	w.restartedAt = w.now()
}

func (w *adbWatchdog) endIncident(ready map[string]struct{}) {
	w.restartedAt = time.Time{}
	w.followUps = 0
	w.expected = ready
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

func readyADBTransports(output string) map[string]struct{} {
	ready := make(map[string]struct{})
	for _, device := range parseDevicesOutput(output, "", nil) {
		if device.State == "device" {
			ready[device.Serial] = struct{}{}
		}
	}
	return ready
}

// replaceADBServer kills the process listening on the adb port, then brings a
// server back the way startup does, so a node that serves its adb port to peers
// gets that server again rather than a loopback-only one a stray client spawned.
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
	return n.EnsureADBServer(ctx)
}
