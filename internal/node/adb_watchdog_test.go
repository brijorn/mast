package node

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

type watchdogHarness struct {
	watchdog *adbWatchdog
	now      time.Time
	output   string
	err      error
	replaced int
	stalled  bool
}

func newWatchdogHarness() *watchdogHarness {
	h := &watchdogHarness{now: time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC)}
	h.watchdog = &adbWatchdog{
		probe: func(context.Context) ([]byte, error) {
			if h.err != nil {
				return nil, h.err
			}
			return []byte(h.output), nil
		},
		replace: func(context.Context) error {
			h.replaced++
			return nil
		},
		now:     func() time.Time { return h.now },
		stalled: func(stalled bool) { h.stalled = stalled },
	}
	return h
}

func (h *watchdogHarness) step(advance time.Duration) {
	h.now = h.now.Add(advance)
	h.watchdog.step(context.Background())
}

const sixPhonesReady = "List of devices attached\n" +
	"1A091FDF600KW6\tdevice\n1A121FDF6009EU\tdevice\n1B081FDF600524\tdevice\n" +
	"R5CTC23TKZZ\tdevice\nR5GL5026YKY\tdevice\nR5GL5029RMF\tdevice\n"

var hungServerError = fmt.Errorf("adb devices: %w", context.DeadlineExceeded)

// The 2026-10-13 hang on finn: the server kept its port and answered nothing.
// One slow answer is a busy server; only a run of them is a hung one.
func TestADBWatchdogReplacesServerAfterConsecutiveUnansweredProbes(t *testing.T) {
	h := newWatchdogHarness()
	h.output = sixPhonesReady
	h.step(0)

	h.err = hungServerError
	for probe := 1; probe < adbWatchdogFailureThreshold; probe++ {
		h.step(adbWatchdogInterval)
		if h.replaced != 0 {
			t.Fatalf("replaced after %d unanswered probes, want to wait for %d", probe, adbWatchdogFailureThreshold)
		}
		if !h.stalled {
			t.Fatal("an unanswered probe should mark adb stalled")
		}
	}
	h.step(adbWatchdogInterval)
	if h.replaced != 1 {
		t.Fatalf("replaced = %d, want 1 after %d unanswered probes", h.replaced, adbWatchdogFailureThreshold)
	}

	h.err = nil
	h.step(adbWatchdogInterval)
	if h.stalled {
		t.Fatal("an answered probe should clear the stall")
	}
	if !h.watchdog.restartedAt.IsZero() {
		t.Fatal("every phone came back, so the incident should be over")
	}
}

func TestADBWatchdogAnsweredProbeResetsTheCount(t *testing.T) {
	h := newWatchdogHarness()
	for range 5 {
		h.err = hungServerError
		h.step(adbWatchdogInterval)
		h.step(adbWatchdogInterval)
		h.err = nil
		h.output = sixPhonesReady
		h.step(adbWatchdogInterval)
	}
	if h.replaced != 0 {
		t.Fatalf("replaced = %d; a server that answers between slow probes is busy, not hung", h.replaced)
	}
}

func TestADBWatchdogLeavesOtherADBFailuresAlone(t *testing.T) {
	h := newWatchdogHarness()
	h.err = errors.New(`exec: "adb": executable file not found in %PATH%`)
	for range 10 {
		h.step(adbWatchdogInterval)
	}
	if h.replaced != 0 || h.stalled {
		t.Fatalf("replaced = %d, stalled = %v; a missing adb is not a hung server", h.replaced, h.stalled)
	}
}

func TestADBServerUnresponsiveRecognisesAHeldPort(t *testing.T) {
	for _, message := range []string{
		"adb devices: exit status 1: * daemon not running; starting now at tcp:5037\ncould not read ok from ADB Server\n* failed to start daemon\nadb.exe: failed to check server version: cannot connect to daemon",
		"adb: failed to check server version: protocol fault (couldn't read status): Connection reset by peer",
	} {
		if !adbServerUnresponsive(errors.New(message)) {
			t.Errorf("not recognised as unresponsive: %q", message)
		}
	}
	if adbServerUnresponsive(errors.New("adb: device 'R5CTC23TKZZ' not found")) {
		t.Error("a missing device is not a hung server")
	}
}

// Replacing the server has left phones offline until a second replacement on
// 2026-10-13, and a replacement that cannot bring them back must not loop.
func TestADBWatchdogRetriesAReplacementThatLostThePhonesAndThenStops(t *testing.T) {
	h := newWatchdogHarness()
	h.output = sixPhonesReady
	h.step(0)
	h.err = hungServerError
	for range adbWatchdogFailureThreshold {
		h.step(adbWatchdogInterval)
	}
	if h.replaced != 1 {
		t.Fatalf("replaced = %d, want 1", h.replaced)
	}

	h.err = nil
	h.output = "List of devices attached\n1A091FDF600KW6\toffline\nR5GL5029RMF\tdevice\n"
	h.step(adbWatchdogInterval)
	if h.replaced != 1 {
		t.Fatal("a replacement must be given time to enumerate before it is judged")
	}
	for range 10 {
		h.step(adbWatchdogSettleDelay)
	}
	if want := 1 + adbWatchdogMaxFollowUps; h.replaced != want {
		t.Fatalf("replaced = %d, want %d: one replacement plus %d follow-ups, then stop", h.replaced, want, adbWatchdogMaxFollowUps)
	}
	if !h.watchdog.restartedAt.IsZero() {
		t.Fatal("the incident should end once the follow-ups are spent")
	}
}

func TestADBWatchdogAcceptsAReplacementThatFoundMostPhones(t *testing.T) {
	h := newWatchdogHarness()
	h.output = sixPhonesReady
	h.step(0)
	h.err = hungServerError
	for range adbWatchdogFailureThreshold {
		h.step(adbWatchdogInterval)
	}

	h.err = nil
	h.output = "List of devices attached\n1A091FDF600KW6\tdevice\n1A121FDF6009EU\tdevice\n1B081FDF600524\tdevice\n"
	h.step(adbWatchdogSettleDelay)
	if h.replaced != 1 {
		t.Fatalf("replaced = %d; three of six back is not worth cutting all three off again", h.replaced)
	}
}

// finn's own netstat on 2026-10-13: a netsh portproxy (svchost, PID 4116)
// listens on the tailnet address, the adb server on loopback, and clients hold
// established connections to both.
func TestParseNetstatListenersFindsOnlyLoopbackAndWildcardListeners(t *testing.T) {
	output := `
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1180
  TCP    100.108.95.85:5037     0.0.0.0:0              LISTENING       4116
  TCP    100.108.95.85:5037     100.98.45.21:44370     ESTABLISHED     4116
  TCP    127.0.0.1:5037         0.0.0.0:0              LISTENING       8460
  TCP    127.0.0.1:5037         127.0.0.1:56653        ESTABLISHED     8460
  TCP    127.0.0.1:56653        127.0.0.1:5037         ESTABLISHED     2848
  TCP    [::1]:5037             [::]:0                 ABHÖREN         8460
  UDP    0.0.0.0:5037           *:*                                    999
`
	if diff := cmp.Diff([]int{8460}, parseNetstatListeners(output, 5037)); diff != "" {
		t.Fatalf("listeners mismatch (-want +got):\n%s", diff)
	}
}

func TestParseTasklistImageName(t *testing.T) {
	if got := parseTasklistImageName(`"adb.exe","8460","Services","0","12,904 K"` + "\r\n"); got != "adb.exe" {
		t.Fatalf("image = %q, want adb.exe", got)
	}
	if got := parseTasklistImageName("INFO: No tasks are running which match the specified criteria.\r\n"); got != "" {
		t.Fatalf("image = %q for a PID with no process, want empty", got)
	}
}
