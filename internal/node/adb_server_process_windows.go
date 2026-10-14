//go:build windows

package node

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// findADBServerProcesses returns the adb processes listening on port. A node can
// have something else on that port too — finn forwards its tailnet address to
// the loopback server with a netsh portproxy, which listens through svchost — so
// only listeners on a loopback or wildcard address that are adb.exe count.
func findADBServerProcesses(port int) ([]int, error) {
	output, err := exec.Command("netstat", "-ano").Output()
	if err != nil {
		return nil, fmt.Errorf("netstat: %w", err)
	}
	var pids []int
	for _, pid := range parseNetstatListeners(string(output), port) {
		image, err := processImageName(pid)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(image, "adb.exe") {
			pids = append(pids, pid)
		}
	}
	if len(pids) == 0 {
		return nil, errADBServerNotFound
	}
	return pids, nil
}

func processImageName(pid int) (string, error) {
	output, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return "", fmt.Errorf("tasklist for pid %d: %w", pid, err)
	}
	return parseTasklistImageName(string(output)), nil
}

var restartUSBDevice = restartUSBDeviceWithPnputil

// restartUSBDeviceWithPnputil restarts the USB device whose serial is the
// phone's. Mast's scheduled task runs with highest privileges, which a device
// restart needs.
func restartUSBDeviceWithPnputil(ctx context.Context, serial string) error {
	listing, err := exec.CommandContext(ctx, "pnputil", "/enum-devices", "/connected").Output()
	if err != nil {
		return fmt.Errorf("pnputil /enum-devices: %w", err)
	}
	id, ok := parsePnputilUSBInstanceID(string(listing), serial)
	if !ok {
		return errDeviceNotConnected
	}
	output, err := exec.CommandContext(ctx, "pnputil", "/restart-device", id).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return fmt.Errorf("pnputil /restart-device %s: %w: %s", id, err, text)
	}
	// pnputil exits zero when it only scheduled the restart for the next boot,
	// which happens while a process still has the device open.
	if strings.Contains(strings.ToLower(text), "reboot") {
		return fmt.Errorf("pnputil /restart-device %s: %s", id, text)
	}
	return nil
}
