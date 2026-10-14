package node

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

type watchdogHarness struct {
	watchdog  *adbWatchdog
	now       time.Time
	output    string
	err       error
	replaced  int
	restarted []string
	restartFn func(serial string, state string) error
	stalled   bool
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
		restartDevice: func(_ context.Context, serial string, state string) error {
			h.restarted = append(h.restarted, serial)
			if h.restartFn != nil {
				return h.restartFn(serial, state)
			}
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

// After 20:29 on 2026-10-13: the server answered, and three of finn's six
// phones were missing while Windows still had all six connected.
const threePhonesReady = "List of devices attached\n" +
	"1A091FDF600KW6\tdevice\n1B081FDF600524\tdevice\nR5GL5026YKY\tdevice\n"

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

// A pnputil restart of each missing phone brought all three back at 20:33 on
// 2026-10-13 while the three that were working kept their transports.
func TestADBWatchdogRestartsOnlyTheMissingPhones(t *testing.T) {
	h := newWatchdogHarness()
	h.output = sixPhonesReady
	h.step(0)

	h.output = threePhonesReady
	h.step(adbWatchdogInterval)
	if len(h.restarted) != 0 {
		t.Fatalf("restarted %v after one listing; it can be a phone caught mid-reconnect", h.restarted)
	}
	h.step(adbWatchdogInterval)
	want := []string{"1A121FDF6009EU", "R5CTC23TKZZ", "R5GL5029RMF"}
	got := append([]string(nil), h.restarted...)
	slices.Sort(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("restarted devices mismatch (-want +got):\n%s", diff)
	}
	if h.replaced != 0 {
		t.Fatal("phones missing from an answering server must not cost the working phones their transports")
	}

	h.output = sixPhonesReady
	h.step(adbWatchdogInterval)
	h.output = threePhonesReady
	h.step(adbWatchdogInterval)
	h.step(adbWatchdogInterval)
	if len(h.restarted) != 6 {
		t.Fatalf("restarts = %d; phones that came back start a fresh count when they go again", len(h.restarted))
	}
}

func TestADBWatchdogGivesUpOnAPhoneAfterItsRestarts(t *testing.T) {
	h := newWatchdogHarness()
	h.output = sixPhonesReady
	h.step(0)
	h.output = "List of devices attached\n1A091FDF600KW6\tdevice\n1A121FDF6009EU\tdevice\n1B081FDF600524\tdevice\nR5CTC23TKZZ\tdevice\nR5GL5026YKY\tdevice\n"
	for range 40 {
		h.step(adbWatchdogInterval)
	}
	if len(h.restarted) != adbWatchdogMaxDeviceRestarts {
		t.Fatalf("restarts = %d, want %d and then leave it to an operator", len(h.restarted), adbWatchdogMaxDeviceRestarts)
	}
	if _, expected := h.watchdog.expected["R5GL5029RMF"]; expected {
		t.Fatal("a phone given up on should no longer be expected")
	}
}

func TestADBWatchdogBacksOffBetweenRestartsOfOnePhone(t *testing.T) {
	h := newWatchdogHarness()
	h.output = "List of devices attached\nR5GL5029RMF\tdevice\n"
	h.step(0)
	h.output = "List of devices attached\n"
	h.step(adbWatchdogInterval)
	h.step(adbWatchdogInterval)
	h.step(adbWatchdogInterval)
	if len(h.restarted) != 1 {
		t.Fatalf("restarts = %d within %s, want 1", len(h.restarted), adbWatchdogDeviceRestartBackoff)
	}
	h.step(adbWatchdogDeviceRestartBackoff)
	if len(h.restarted) != 2 {
		t.Fatalf("restarts = %d after the backoff, want 2", len(h.restarted))
	}
}

func TestADBWatchdogStopsExpectingAnUnpluggedPhone(t *testing.T) {
	h := newWatchdogHarness()
	h.output = sixPhonesReady
	h.step(0)
	h.restartFn = func(string, string) error { return errDeviceNotConnected }
	h.output = threePhonesReady
	for range 10 {
		h.step(adbWatchdogInterval)
	}
	if len(h.restarted) != 3 {
		t.Fatalf("restarts = %d; a phone Windows no longer has is tried once", len(h.restarted))
	}
	if len(h.watchdog.expected) != 3 {
		t.Fatalf("expected %d phones, want the 3 still connected", len(h.watchdog.expected))
	}
}

func TestADBWatchdogLeavesWirelessAndUnauthorizedPhonesAlone(t *testing.T) {
	h := newWatchdogHarness()
	h.output = "List of devices attached\n192.168.1.159:38261\tdevice\nemulator-5554\tdevice\nR5GL5029RMF\tdevice\n"
	h.step(0)
	h.output = "List of devices attached\nR5GL5029RMF\tunauthorized\n"
	for range 10 {
		h.step(adbWatchdogInterval)
	}
	if len(h.restarted) != 0 {
		t.Fatalf("restarted %v; a wireless link has no USB device and an unauthorized phone needs a person", h.restarted)
	}
}

func TestADBWatchdogWithoutDeviceRestartsOnlyWatchesForAHang(t *testing.T) {
	h := newWatchdogHarness()
	h.watchdog.restartDevice = nil
	h.output = sixPhonesReady
	h.step(0)
	h.output = "List of devices attached\n"
	for range 10 {
		h.step(adbWatchdogInterval)
	}
	if h.replaced != 0 {
		t.Fatalf("replaced = %d; an answering server is not replaced", h.replaced)
	}
}

// 20:20 on 2026-10-13: finn's transports ended twelve seconds before the new
// Mast started, so the new process's first look already showed an emptied rack.
func TestADBWatchdogRemembersThePhoneSetAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), adbWatchdogFileName)
	before := newWatchdogHarness()
	before.watchdog.path = path
	before.output = sixPhonesReady
	before.step(0)

	after := newWatchdogHarness()
	after.now = before.now.Add(time.Minute)
	after.watchdog.path = path
	after.watchdog.load()
	after.output = "List of devices attached\n"
	after.step(adbWatchdogInterval)
	after.step(adbWatchdogInterval)
	if len(after.restarted) != 6 {
		t.Fatalf("restarts = %d; the restarted watchdog should know six phones were ready", len(after.restarted))
	}
}

func TestADBWatchdogForgetsAStalePhoneSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), adbWatchdogFileName)
	before := newWatchdogHarness()
	before.watchdog.path = path
	before.output = sixPhonesReady
	before.step(0)

	after := newWatchdogHarness()
	after.now = before.now.Add(adbWatchdogMemoryMaxAge + time.Minute)
	after.watchdog.path = path
	after.watchdog.load()
	after.output = "List of devices attached\n"
	for range 5 {
		after.step(adbWatchdogInterval)
	}
	if len(after.restarted) != 0 {
		t.Fatalf("restarted %v; a set saved before the node was down this long may describe another rack", after.restarted)
	}
}

func TestADBWatchdogRefreshesASteadyPhoneSet(t *testing.T) {
	h := newWatchdogHarness()
	h.watchdog.path = filepath.Join(t.TempDir(), adbWatchdogFileName)
	h.output = sixPhonesReady
	h.step(0)
	first := h.watchdog.savedAt
	h.step(adbWatchdogMemoryRefresh)
	if !h.watchdog.savedAt.After(first) {
		t.Fatal("an unchanged set must still be saved again, or a steady rack ages out of memory")
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

// finn's pnputil on 2026-10-13. The serial is the last segment of the parent
// USB device's instance ID; its interfaces carry generated IDs instead.
func TestParsePnputilUSBInstanceID(t *testing.T) {
	output := "Microsoft PnP Utility\r\n\r\n" +
		"Instance ID:                USB\\VID_04E8&PID_6860&MI_00\\6&3b656954&0&0000\r\n" +
		"Device Description:         Galaxy S22\r\n\r\n" +
		"Instance ID:                USB\\VID_04E8&PID_6860\\R5GL5029RMF\r\n" +
		"Device Description:         SAMSUNG Mobile USB Composite Device \r\n" +
		"Status:                     Started\r\n\r\n" +
		"Instance ID:                USB\\VID_18D1&PID_4EE7\\1A121FDF6009EU\r\n" +
		"Device Description:         Pixel 6\r\n"
	if id, ok := parsePnputilUSBInstanceID(output, "R5GL5029RMF"); !ok || id != `USB\VID_04E8&PID_6860\R5GL5029RMF` {
		t.Fatalf("id = %q, %v", id, ok)
	}
	if id, ok := parsePnputilUSBInstanceID(output, "1a121fdf6009eu"); !ok || id != `USB\VID_18D1&PID_4EE7\1A121FDF6009EU` {
		t.Fatalf("id = %q, %v; serials match without regard to case", id, ok)
	}
	if _, ok := parsePnputilUSBInstanceID(output, "R5CTC23TKZZ"); ok {
		t.Fatal("a serial with no connected device must not match")
	}
	if _, ok := parsePnputilUSBInstanceID(output, "0000"); ok {
		t.Fatal("an interface's generated ID is not a serial")
	}
}
