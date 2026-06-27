package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brijorn/mast/internal/transport"
)

type DeviceInfo struct {
	Serial                     string   `json:"serial"`
	State                      string   `json:"state"`
	BatteryPercent             *int     `json:"battery_percent,omitempty"`
	PowerConnected             *bool    `json:"power_connected,omitempty"`
	PowerSource                string   `json:"power_source,omitempty"`
	BatteryStatus              string   `json:"battery_status,omitempty"`
	PowerHealth                string   `json:"power_health,omitempty"`
	BatteryCurrentNow          *int     `json:"battery_current_now,omitempty"`
	BatteryCurrentAvg          *int     `json:"battery_current_avg,omitempty"`
	BatteryTrendPercentPerHour *float64 `json:"battery_trend_percent_per_hour,omitempty"`
	NodeID                     string   `json:"node_id"`
}

const peerDeviceRPCTimeout = 10 * time.Second

type adbRunner interface {
	Devices(host string) ([]byte, error)
	Push(host string, localPath string, remotePath string) error
	Reverse(host string, deviceSocket string, localPort int) error
	StartShell(host string, arg ...string) (*exec.Cmd, error)
	Shell(host string, serial string, arg ...string) ([]byte, error)
	ExecOut(host string, serial string, arg ...string) ([]byte, error)
}

type realADB struct{}

type batterySnapshot struct {
	BatteryPercent             *int
	PowerConnected             *bool
	PowerSource                string
	BatteryStatus              string
	PowerHealth                string
	BatteryCurrentNow          *int
	BatteryCurrentAvg          *int
	BatteryTrendPercentPerHour *float64
}

var batteryLogLinePattern = regexp.MustCompile(`^(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2}).*level:(\d+).*current_avg:([-+]?\d+)`)

func (a realADB) run(host string, arg ...string) ([]byte, error) {
	args := adbArgs(host, arg...)
	output, err := exec.Command("adb", args...).CombinedOutput()
	if err != nil {
		return output, commandError("adb", args, output, err)
	}
	return output, nil
}

func (a realADB) Devices(host string) ([]byte, error) {
	return a.run(host, "devices")
}

func (a realADB) Push(host string, localPath string, remotePath string) error {
	_, err := a.run(host, "push", localPath, remotePath)
	return err
}

func (a realADB) Reverse(host string, deviceSocket string, localPort int) error {
	_, err := a.run(host, "reverse", deviceSocket, "tcp:"+strconv.Itoa(localPort))
	return err
}

func (a realADB) StartShell(host string, arg ...string) (*exec.Cmd, error) {
	args := adbArgs(host)
	args = append(args, "shell")
	args = append(args, arg...)
	cmd := exec.Command("adb", args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (a realADB) Shell(host string, serial string, arg ...string) ([]byte, error) {
	var args []string
	if serial != "" {
		args = append(args, "-s", serial)
	}
	args = append(args, "shell")
	args = append(args, arg...)
	return a.run(host, args...)
}

func (a realADB) ExecOut(host string, serial string, arg ...string) ([]byte, error) {
	var args []string
	if serial != "" {
		args = append(args, "-s", serial)
	}
	args = append(args, "exec-out")
	args = append(args, arg...)
	return a.run(host, args...)
}

func adbArgs(host string, arg ...string) []string {
	var args []string
	if host != "" {
		args = append(args, "-H", host, "-P", "5037")
	}
	return append(args, arg...)
}

func commandError(name string, args []string, output []byte, err error) error {
	command := strings.TrimSpace(name + " " + strings.Join(args, " "))
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("%s: %w", command, err)
	}
	return fmt.Errorf("%s: %w: %s", command, err, detail)
}

func (n *Node) ListDevices() ([]DeviceInfo, error) {
	devices, err := n.listLocalDevices()
	if err != nil {
		return nil, err
	}

	for _, peerID := range n.androidPeerIDs() {
		peerDevices, err := n.listPeerDevices(n.ctx, peerID)
		if err != nil {
			return nil, err
		}
		devices = append(devices, peerDevices...)
	}

	return devices, nil
}

func parseBatteryLevel(output string) (*int, error) {
	battery, err := parseBatterySnapshot(output)
	if err != nil {
		return nil, err
	}
	return battery.BatteryPercent, nil
}

func parseBatterySnapshot(output string) (batterySnapshot, error) {
	var snapshot batterySnapshot
	var acPowered, usbPowered, wirelessPowered, dockPowered *bool
	var rawStatus *int

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "AC powered:"):
			acPowered = parseBatteryBool(line)
		case strings.HasPrefix(line, "USB powered:"):
			usbPowered = parseBatteryBool(line)
		case strings.HasPrefix(line, "Wireless powered:"):
			wirelessPowered = parseBatteryBool(line)
		case strings.HasPrefix(line, "Dock powered:"):
			dockPowered = parseBatteryBool(line)
		case strings.HasPrefix(line, "status:"):
			rawStatus = parseBatteryInt(line)
		case strings.HasPrefix(line, "level:"):
			snapshot.BatteryPercent = parseBatteryInt(line)
		case strings.HasPrefix(line, "current now:"):
			snapshot.BatteryCurrentNow = parseBatteryInt(line)
		}
	}

	if rawStatus != nil {
		snapshot.BatteryStatus = batteryStatusName(*rawStatus)
	}

	snapshot.PowerSource = powerSourceName(acPowered, usbPowered, wirelessPowered, dockPowered)
	if acPowered != nil || usbPowered != nil || wirelessPowered != nil || dockPowered != nil {
		connected := snapshot.PowerSource != "none"
		snapshot.PowerConnected = &connected
	}

	trend, latestCurrentAvg := parseBatteryTrend(output)
	snapshot.BatteryTrendPercentPerHour = trend
	snapshot.BatteryCurrentAvg = latestCurrentAvg
	snapshot.PowerHealth = derivePowerHealth(snapshot)

	return snapshot, nil
}

func parseBatteryBool(line string) *bool {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return nil
	}
	switch strings.TrimSpace(parts[1]) {
	case "true":
		value := true
		return &value
	case "false":
		value := false
		return &value
	default:
		return nil
	}
}

func parseBatteryInt(line string) *int {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil
	}
	return &value
}

func batteryStatusName(status int) string {
	switch status {
	case 2:
		return "charging"
	case 3:
		return "discharging"
	case 4:
		return "not_charging"
	case 5:
		return "full"
	default:
		return "unknown"
	}
}

func powerSourceName(acPowered, usbPowered, wirelessPowered, dockPowered *bool) string {
	switch {
	case acPowered != nil && *acPowered:
		return "ac"
	case usbPowered != nil && *usbPowered:
		return "usb"
	case wirelessPowered != nil && *wirelessPowered:
		return "wireless"
	case dockPowered != nil && *dockPowered:
		return "dock"
	case acPowered != nil || usbPowered != nil || wirelessPowered != nil || dockPowered != nil:
		return "none"
	default:
		return ""
	}
}

func parseBatteryTrend(output string) (*float64, *int) {
	type sample struct {
		t          time.Time
		level      int
		currentAvg int
	}
	var samples []sample

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		matches := batteryLogLinePattern.FindStringSubmatch(line)
		if len(matches) != 8 {
			continue
		}
		month, err1 := strconv.Atoi(matches[1])
		day, err2 := strconv.Atoi(matches[2])
		hour, err3 := strconv.Atoi(matches[3])
		minute, err4 := strconv.Atoi(matches[4])
		second, err5 := strconv.Atoi(matches[5])
		level, err6 := strconv.Atoi(matches[6])
		currentAvg, err7 := strconv.Atoi(matches[7])
		if err := firstErr(err1, err2, err3, err4, err5, err6, err7); err != nil {
			continue
		}
		samples = append(samples, sample{
			t:          time.Date(2000, time.Month(month), day, hour, minute, second, 0, time.UTC),
			level:      level,
			currentAvg: currentAvg,
		})
	}

	if len(samples) == 0 {
		return nil, nil
	}

	latestCurrentAvg := samples[len(samples)-1].currentAvg
	if len(samples) < 2 {
		return nil, &latestCurrentAvg
	}

	first := samples[0]
	last := samples[len(samples)-1]
	hours := last.t.Sub(first.t).Hours()
	if hours <= 0 {
		return nil, &latestCurrentAvg
	}
	trend := float64(last.level-first.level) / hours
	return &trend, &latestCurrentAvg
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func derivePowerHealth(snapshot batterySnapshot) string {
	if snapshot.BatteryStatus == "" && snapshot.PowerConnected == nil {
		return ""
	}
	if snapshot.BatteryStatus == "full" {
		return "full"
	}

	if snapshot.PowerConnected != nil && *snapshot.PowerConnected {
		if snapshot.BatteryTrendPercentPerHour != nil && *snapshot.BatteryTrendPercentPerHour < -0.1 {
			return "plugged_draining"
		}
		if snapshot.BatteryCurrentNow != nil && *snapshot.BatteryCurrentNow < 0 {
			return "plugged_draining"
		}
		if snapshot.BatteryStatus == "charging" {
			return "charging"
		}
		return "holding"
	}

	if snapshot.PowerConnected != nil && !*snapshot.PowerConnected {
		if snapshot.BatteryStatus == "discharging" ||
			(snapshot.BatteryTrendPercentPerHour != nil && *snapshot.BatteryTrendPercentPerHour < -0.1) ||
			(snapshot.BatteryCurrentNow != nil && *snapshot.BatteryCurrentNow < 0) {
			return "unplugged_draining"
		}
		return "unknown"
	}

	return "unknown"
}

func (n *Node) cacheBattery(serial string, snapshot batterySnapshot) {
	n.batteryMu.Lock()
	defer n.batteryMu.Unlock()
	if n.batteryCache == nil {
		n.batteryCache = make(map[string]batterySnapshot)
	}
	n.batteryCache[serial] = snapshot
}

func (n *Node) cachedBattery(serial string) (batterySnapshot, bool) {
	n.batteryMu.RLock()
	defer n.batteryMu.RUnlock()
	snapshot, ok := n.batteryCache[serial]
	return snapshot, ok
}

func (n *Node) deviceBattery(serial string) (batterySnapshot, error) {
	output, err := n.adb.Shell("", serial, "dumpsys", "battery")
	if err != nil {
		return batterySnapshot{}, err
	}

	return parseBatterySnapshot(string(output))
}

func (n *Node) listLocalDeviceStates() ([]DeviceInfo, error) {
	rawOutput, err := n.adb.Devices("")
	if err != nil {
		return nil, err
	}

	return parseDevicesOutput(string(rawOutput), n.ID, nil), nil
}

func (n *Node) listLocalDevices() ([]DeviceInfo, error) {
	devices, err := n.listLocalDeviceStates()
	if err != nil {
		return nil, err
	}

	for i := range devices {
		if devices[i].State != "device" {
			continue
		}

		battery, err := n.deviceBattery(devices[i].Serial)
		if err != nil {
			log.Printf("get battery for %s: %v", devices[i].Serial, err)
			if cached, ok := n.cachedBattery(devices[i].Serial); ok {
				applyBatterySnapshot(&devices[i], cached)
			}
			continue
		}
		n.cacheBattery(devices[i].Serial, battery)
		applyBatterySnapshot(&devices[i], battery)
	}

	return devices, nil
}

func (n *Node) androidPeerIDs() []string {
	var peerIDs []string
	n.mu.RLock()
	defer n.mu.RUnlock()
	for id, peer := range n.Peers {
		if !peer.AndroidEnabled {
			continue
		}
		peerIDs = append(peerIDs, id)
	}
	return peerIDs
}

func (n *Node) listPeerDevices(ctx context.Context, peerID string) ([]DeviceInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeListDevicesRequest, nil)
	if err != nil {
		return nil, fmt.Errorf("list devices from peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeListDevicesResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.ListDevicesResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("list devices from peer %s: %s", peerID, res.Payload.Error)
	}

	return deviceInfosFromPayload(res.Payload.Result), nil
}

func (n *Node) handleListDevicesRequest(peer *PeerConn, req transport.ListDevicesRequest) {
	devices, err := n.listLocalDevices()
	payload := transport.ListDevicesResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = deviceInfoPayloads(devices)
	}

	n.writePeerResponse(peer, transport.TypeListDevicesResponse, req.RawMessage, payload)
}

func (n *Node) Screenshot(serial string) ([]byte, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.NodeID == n.ID {
		return n.localScreenshot(serial)
	}
	return n.peerScreenshot(n.ctx, device.NodeID, serial)
}

func (n *Node) localScreenshot(serial string) ([]byte, error) {
	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.State != "device" {
		return nil, fmt.Errorf("device %s is %s", serial, device.State)
	}
	return n.adb.ExecOut("", serial, "screencap", "-p")
}

func (n *Node) peerScreenshot(ctx context.Context, peerID string, serial string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.ScreenshotRequestPayload{Serial: serial}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeScreenshotRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("screenshot from peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeScreenshotResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.ScreenshotResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("screenshot from peer %s: %s", peerID, res.Payload.Error)
	}
	return res.Payload.PNG, nil
}

func (n *Node) handleScreenshotRequest(peer *PeerConn, req transport.ScreenshotRequest) {
	png, err := n.localScreenshot(req.Payload.Serial)
	payload := transport.ScreenshotResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.PNG = png
	}
	n.writePeerResponse(peer, transport.TypeScreenshotResponse, req.RawMessage, payload)
}

func (n *Node) DeviceBySerial(serial string) (*DeviceInfo, error) {
	devices, err := n.ListDevices()
	if err != nil {
		return nil, err
	}

	index := slices.IndexFunc(devices, func(d DeviceInfo) bool {
		return d.Serial == serial
	})
	if index == -1 {
		return nil, errors.New("device not found:" + serial)
	}

	return &devices[index], nil
}

func (n *Node) localDeviceBySerial(serial string) (*DeviceInfo, error) {
	devices, err := n.listLocalDeviceStates()
	if err != nil {
		return nil, err
	}

	index := slices.IndexFunc(devices, func(d DeviceInfo) bool {
		return d.Serial == serial
	})
	if index == -1 {
		return nil, errors.New("device not found:" + serial)
	}

	return &devices[index], nil
}

func deviceInfoPayloads(devices []DeviceInfo) []transport.DeviceInfoPayload {
	payloads := make([]transport.DeviceInfoPayload, 0, len(devices))
	for _, device := range devices {
		payloads = append(payloads, transport.DeviceInfoPayload{
			Serial:                     device.Serial,
			State:                      device.State,
			NodeID:                     device.NodeID,
			BatteryPercent:             device.BatteryPercent,
			PowerConnected:             device.PowerConnected,
			PowerSource:                device.PowerSource,
			BatteryStatus:              device.BatteryStatus,
			PowerHealth:                device.PowerHealth,
			BatteryCurrentNow:          device.BatteryCurrentNow,
			BatteryCurrentAvg:          device.BatteryCurrentAvg,
			BatteryTrendPercentPerHour: device.BatteryTrendPercentPerHour,
		})
	}
	return payloads
}

func deviceInfosFromPayload(payloads []transport.DeviceInfoPayload) []DeviceInfo {
	devices := make([]DeviceInfo, 0, len(payloads))
	for _, payload := range payloads {
		devices = append(devices, DeviceInfo{
			Serial:                     payload.Serial,
			State:                      payload.State,
			NodeID:                     payload.NodeID,
			BatteryPercent:             payload.BatteryPercent,
			PowerConnected:             payload.PowerConnected,
			PowerSource:                payload.PowerSource,
			BatteryStatus:              payload.BatteryStatus,
			PowerHealth:                payload.PowerHealth,
			BatteryCurrentNow:          payload.BatteryCurrentNow,
			BatteryCurrentAvg:          payload.BatteryCurrentAvg,
			BatteryTrendPercentPerHour: payload.BatteryTrendPercentPerHour,
		})
	}
	return devices
}

func applyBatterySnapshot(device *DeviceInfo, snapshot batterySnapshot) {
	device.BatteryPercent = snapshot.BatteryPercent
	device.PowerConnected = snapshot.PowerConnected
	device.PowerSource = snapshot.PowerSource
	device.BatteryStatus = snapshot.BatteryStatus
	device.PowerHealth = snapshot.PowerHealth
	device.BatteryCurrentNow = snapshot.BatteryCurrentNow
	device.BatteryCurrentAvg = snapshot.BatteryCurrentAvg
	device.BatteryTrendPercentPerHour = snapshot.BatteryTrendPercentPerHour
}

func parseDevicesOutput(output string, nodeID string, devices []DeviceInfo) []DeviceInfo {

	lines := strings.Split(output, "\n")
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			continue
		}
		devices = append(devices, DeviceInfo{
			Serial: strings.TrimSpace(parts[0]),
			State:  strings.TrimSpace(parts[1]),
			NodeID: nodeID,
		})
	}
	return devices
}
