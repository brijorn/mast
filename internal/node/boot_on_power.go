package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/brijorn/mast/internal/transport"
)

// Boot on power is whether a phone that is switched off starts Android as soon
// as USB power arrives, instead of showing its charging screen. With it on, a
// phone that died or was shut down comes back by cycling its port's power, so
// nobody has to hold its power button.
//
// It is not an Android setting. Pixels and Motorolas keep it in the bootloader
// as "off-mode charge", readable and writable only from fastboot, so the phone
// reboots into its bootloader, the flag is written and read back, and the phone
// reboots into Android. fastboot talks to the phone over USB, which is why this
// runs on the node the phone is plugged into and is forwarded there like any
// other device control. Samsungs have no fastboot; their bootloader refuses
// rather than ignoring, and that refusal is the error the caller sees.
//
// Written on a locked Pixel 6 (1A091FDF600KW6) on 2026-10-23: off-mode-charge
// read 1, `oem off-mode-charge 0` answered OKAY, and it read 0 afterwards.

type BootOnPowerStatus struct {
	Serial string `json:"serial"`
	// Enabled is what the bootloader reported after the write: true when the
	// phone now boots on power. Nil when it would not say.
	Enabled *bool `json:"enabled,omitempty"`
	// Before is the off-mode-charge value read before the write, "" if unread.
	Before string `json:"before"`
	After  string `json:"after"`
}

var (
	fastbootCommandTimeout = 20 * time.Second
	bootloaderArrivalWait  = 60 * time.Second
	bootloaderPollInterval = 2 * time.Second
	execFastbootCommand    = exec.CommandContext
	lookPathFastboot       = func() (string, error) { return exec.LookPath("fastboot") }
)

var offModeChargePattern = regexp.MustCompile(`(?m)off-mode-charge:\s*([01])`)

// SetDeviceBootOnPower turns boot-on-power on (enabled) or back to the
// charging screen (disabled).
func (n *Node) SetDeviceBootOnPower(serial string, enabled bool) (*BootOnPowerStatus, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.Platform != PlatformAndroid {
		return nil, fmt.Errorf("boot on power is an Android bootloader setting; %s is %s", serial, device.Platform)
	}
	if device.NodeID == n.ID {
		return n.setLocalDeviceBootOnPower(serial, enabled)
	}
	return n.setPeerDeviceBootOnPower(n.ctx, device.NodeID, serial, enabled)
}

func (n *Node) setLocalDeviceBootOnPower(serial string, enabled bool) (*BootOnPowerStatus, error) {
	if err := n.requireLocalReadyDevice(serial); err != nil {
		return nil, err
	}
	// fastboot needs the phone on this machine's USB; a wireless-debugging
	// phone has no bootloader connection to reach.
	if address := n.deviceAddress(serial); strings.Contains(address, ":") {
		return nil, fmt.Errorf("%s is connected over wireless debugging; boot on power needs it on USB", serial)
	}
	fastboot, err := lookPathFastboot()
	if err != nil {
		return nil, fmt.Errorf("fastboot is not installed on node %s", n.ID)
	}
	ctx := n.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	rebootCtx, cancel := context.WithTimeout(ctx, adbCommandTimeout)
	cmd := execADBCommand(rebootCtx, "adb", adbSerialArgs(n.deviceAddress(serial), "reboot", "bootloader")...)
	configureADBCommandCancellation(cmd)
	output, err := cmd.CombinedOutput()
	cancel()
	if err != nil {
		return nil, commandError("adb", []string{"-s", serial, "reboot", "bootloader"}, output, err)
	}
	if err := waitForBootloader(ctx, fastboot, serial); err != nil {
		return nil, err
	}

	// From here the phone sits in its bootloader, so every exit reboots it,
	// whatever else failed: a phone left in fastboot is a phone off the fleet.
	status := &BootOnPowerStatus{Serial: serial}
	var setErr error
	func() {
		status.Before = readOffModeCharge(ctx, fastboot, serial)
		value := "1"
		if enabled {
			value = "0"
		}
		out, err := runFastboot(ctx, fastboot, serial, "oem", "off-mode-charge", value)
		if err != nil {
			setErr = err
			return
		}
		if !strings.Contains(string(out), "OKAY") {
			setErr = fmt.Errorf("the bootloader did not accept off-mode-charge %s: %s", value, strings.TrimSpace(string(out)))
			return
		}
		status.After = readOffModeCharge(ctx, fastboot, serial)
		if status.After != "" {
			on := status.After == "0"
			status.Enabled = &on
		}
	}()
	if _, err := runFastboot(ctx, fastboot, serial, "reboot"); err != nil && setErr == nil {
		setErr = fmt.Errorf("set, but the phone did not reboot out of its bootloader: %w", err)
	}
	if setErr != nil {
		return status, setErr
	}
	if status.Enabled != nil && *status.Enabled != enabled {
		return status, fmt.Errorf("the bootloader accepted the write but still reads off-mode-charge %s", status.After)
	}
	return status, nil
}

func runFastboot(ctx context.Context, fastboot string, serial string, arg ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fastbootCommandTimeout)
	defer cancel()
	args := append([]string{"-s", serial}, arg...)
	cmd := execFastbootCommand(ctx, fastboot, args...)
	configureADBCommandCancellation(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, commandError("fastboot", args, output, err)
	}
	return output, nil
}

// readOffModeCharge is "1" (charging screen), "0" (boots on power), or "" when
// the bootloader would not say. fastboot prints getvar answers on stderr, which
// CombinedOutput folds in.
func readOffModeCharge(ctx context.Context, fastboot string, serial string) string {
	out, err := runFastboot(ctx, fastboot, serial, "getvar", "off-mode-charge")
	if err != nil {
		return ""
	}
	return parseOffModeCharge(string(out))
}

func parseOffModeCharge(output string) string {
	match := offModeChargePattern.FindStringSubmatch(output)
	if match == nil {
		return ""
	}
	return match[1]
}

func waitForBootloader(ctx context.Context, fastboot string, serial string) error {
	deadline := time.Now().Add(bootloaderArrivalWait)
	for {
		listCtx, cancel := context.WithTimeout(ctx, fastbootCommandTimeout)
		cmd := execFastbootCommand(listCtx, fastboot, "devices")
		configureADBCommandCancellation(cmd)
		out, _ := cmd.CombinedOutput()
		cancel()
		if fastbootListsSerial(string(out), serial) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not reach its bootloader within %s; if it is a Samsung, it has none fastboot can talk to", serial, bootloaderArrivalWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bootloaderPollInterval):
		}
	}
}

func fastbootListsSerial(output string, serial string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == serial && fields[1] == "fastboot" {
			return true
		}
	}
	return false
}

// The bootloader round trip takes up to a couple of minutes, far longer than
// the ten seconds other device controls are allowed.
const peerBootOnPowerTimeout = 4 * time.Minute

func (n *Node) setPeerDeviceBootOnPower(ctx context.Context, peerID string, serial string, enabled bool) (*BootOnPowerStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerBootOnPowerTimeout)
	defer cancel()

	payload := transport.BootOnPowerSetRequestPayload{Serial: serial, Enabled: enabled}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeBootOnPowerSetRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("set boot on power on peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeBootOnPowerSetResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}
	var res transport.BootOnPowerSetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	status := bootOnPowerStatusFromPayload(res.Payload.Result)
	if res.Payload.Error != "" {
		return status, fmt.Errorf("set boot on power on peer %s: %s", peerID, res.Payload.Error)
	}
	return status, nil
}

func (n *Node) handleBootOnPowerSetRequest(peer *PeerConn, req transport.BootOnPowerSetRequest) {
	status, err := n.setLocalDeviceBootOnPower(req.Payload.Serial, req.Payload.Enabled)
	payload := transport.BootOnPowerSetResponsePayload{Result: bootOnPowerStatusPayload(status)}
	if err != nil {
		payload.Error = err.Error()
	}
	n.writePeerResponse(peer, transport.TypeBootOnPowerSetResponse, req.RawMessage, payload)
}

func bootOnPowerStatusPayload(status *BootOnPowerStatus) *transport.BootOnPowerStatusPayload {
	if status == nil {
		return nil
	}
	return &transport.BootOnPowerStatusPayload{Serial: status.Serial, Enabled: status.Enabled, Before: status.Before, After: status.After}
}

func bootOnPowerStatusFromPayload(payload *transport.BootOnPowerStatusPayload) *BootOnPowerStatus {
	if payload == nil {
		return nil
	}
	return &BootOnPowerStatus{Serial: payload.Serial, Enabled: payload.Enabled, Before: payload.Before, After: payload.After}
}
