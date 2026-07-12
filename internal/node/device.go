package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"log"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/brijorn/mast/internal/transport"
)

type DeviceInfo struct {
	Serial   string         `json:"serial"`
	Platform string         `json:"platform"`
	State    string         `json:"state"`
	Battery  *DeviceBattery `json:"battery,omitempty"`
	NodeID   string         `json:"node_id"`
}

type BatteryState string

const (
	BatteryStateCharging        BatteryState = "charging"
	BatteryStateHolding         BatteryState = "holding"
	BatteryStateFull            BatteryState = "full"
	BatteryStateDischarging     BatteryState = "discharging"
	BatteryStatePluggedDraining BatteryState = "plugged_draining"
	BatteryStateUnknown         BatteryState = "unknown"
)

type DeviceBattery struct {
	Percent *int         `json:"percent,omitempty"`
	State   BatteryState `json:"state"`
}

// DeviceGeometry separates captured screenshot pixels from the coordinate
// space accepted by the device control backend.
type DeviceGeometry struct {
	Serial           string `json:"serial"`
	Platform         string `json:"platform"`
	Orientation      string `json:"orientation"`
	ScreenshotWidth  int    `json:"screenshot_width"`
	ScreenshotHeight int    `json:"screenshot_height"`
	InputWidth       int    `json:"input_width"`
	InputHeight      int    `json:"input_height"`
}

const (
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
)

const peerDeviceRPCTimeout = 10 * time.Second

type adbRunner interface {
	Devices(ctx context.Context, host string) ([]byte, error)
	Push(ctx context.Context, host string, serial string, localPath string, remotePath string) error
	Reverse(ctx context.Context, host string, serial string, deviceSocket string, localPort int) error
	StartShell(host string, serial string, arg ...string) (*exec.Cmd, error)
	Shell(ctx context.Context, host string, serial string, arg ...string) ([]byte, error)
	ExecOut(ctx context.Context, host string, serial string, arg ...string) ([]byte, error)
}

type realADB struct{}

type batterySnapshot struct {
	BatteryPercent             *int
	PowerConnected             *bool
	PowerSource                string
	BatteryStatus              string
	BatteryState               BatteryState
	BatteryCurrentNow          *int
	BatteryCurrentAvg          *int
	BatteryTrendPercentPerHour *float64
}

var batteryLogLinePattern = regexp.MustCompile(`^(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2}).*level:(\d+).*current_avg:([-+]?\d+)`)

var (
	adbCommandTimeout  = 10 * time.Second
	adbTransferTimeout = 30 * time.Second
	execADBCommand     = exec.CommandContext
)

func adbContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeout)
}

func (a realADB) run(ctx context.Context, host string, timeout time.Duration, arg ...string) ([]byte, error) {
	ctx, cancel := adbContext(ctx, timeout)
	defer cancel()

	args := adbArgs(host, arg...)
	output, err := execADBCommand(ctx, "adb", args...).CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return output, commandError("adb", args, output, err)
	}
	return output, nil
}

func (a realADB) Devices(ctx context.Context, host string) ([]byte, error) {
	return a.run(ctx, host, adbCommandTimeout, "devices")
}

func (a realADB) Push(ctx context.Context, host string, serial string, localPath string, remotePath string) error {
	args := adbSerialArgs(serial, "push", localPath, remotePath)
	_, err := a.run(ctx, host, adbCommandTimeout, args...)
	return err
}

func (a realADB) Reverse(ctx context.Context, host string, serial string, deviceSocket string, localPort int) error {
	args := adbSerialArgs(serial, "reverse", deviceSocket, "tcp:"+strconv.Itoa(localPort))
	_, err := a.run(ctx, host, adbCommandTimeout, args...)
	return err
}

type SafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *SafeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *SafeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (a realADB) StartShell(host string, serial string, arg ...string) (*exec.Cmd, error) {
	args := adbArgs(host, adbSerialArgs(serial, "shell")...)
	args = append(args, arg...)
	cmd := exec.Command("adb", args...)
	cmd.Stderr = &SafeBuffer{}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (a realADB) Shell(ctx context.Context, host string, serial string, arg ...string) ([]byte, error) {
	args := adbSerialArgs(serial, "shell")
	args = append(args, arg...)
	return a.run(ctx, host, adbCommandTimeout, args...)
}

func (a realADB) ExecOut(ctx context.Context, host string, serial string, arg ...string) ([]byte, error) {
	args := adbSerialArgs(serial, "exec-out")
	args = append(args, arg...)
	return a.run(ctx, host, adbTransferTimeout, args...)
}

func adbArgs(host string, arg ...string) []string {
	var args []string
	if host != "" {
		args = append(args, "-H", host, "-P", "5037")
	}
	return append(args, arg...)
}

func adbSerialArgs(serial string, arg ...string) []string {
	if serial == "" {
		return arg
	}
	args := []string{"-s", serial}
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

	for _, result := range n.listPeerDevicesConcurrently(n.ctx, n.devicePeerIDs()) {
		if result.err != nil {
			log.Printf("list devices from peer %s: %v", result.peerID, result.err)
			n.setPeerDeviceError(result.peerID, result.err.Error())
			continue
		}
		n.setPeerDeviceError(result.peerID, "")
		devices = append(devices, result.devices...)
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
	snapshot.BatteryState = deriveBatteryState(snapshot)

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

func deriveBatteryState(snapshot batterySnapshot) BatteryState {
	if snapshot.BatteryStatus == "" && snapshot.PowerConnected == nil {
		return ""
	}
	if snapshot.BatteryStatus == "full" {
		return BatteryStateFull
	}

	if snapshot.PowerConnected != nil && *snapshot.PowerConnected {
		if snapshot.BatteryTrendPercentPerHour != nil && *snapshot.BatteryTrendPercentPerHour < -0.1 {
			return BatteryStatePluggedDraining
		}
		if snapshot.BatteryCurrentNow != nil && *snapshot.BatteryCurrentNow < 0 {
			return BatteryStatePluggedDraining
		}
		if snapshot.BatteryStatus == "charging" {
			return BatteryStateCharging
		}
		return BatteryStateHolding
	}

	if snapshot.PowerConnected != nil && !*snapshot.PowerConnected {
		if snapshot.BatteryStatus == "discharging" ||
			(snapshot.BatteryTrendPercentPerHour != nil && *snapshot.BatteryTrendPercentPerHour < -0.1) ||
			(snapshot.BatteryCurrentNow != nil && *snapshot.BatteryCurrentNow < 0) {
			return BatteryStateDischarging
		}
		return BatteryStateUnknown
	}

	return BatteryStateUnknown
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
	output, err := n.adb.Shell(n.ctx, "", serial, "dumpsys", "battery")
	if err != nil {
		return batterySnapshot{}, err
	}

	return parseBatterySnapshot(string(output))
}

func (n *Node) listLocalDeviceStates() ([]DeviceInfo, error) {
	var devices []DeviceInfo
	var adbErr error
	if n.AndroidEnabled {
		rawOutput, err := n.adb.Devices(n.ctx, "")
		if err != nil {
			adbErr = err
		} else {
			devices = parseDevicesOutput(string(rawOutput), n.ID, nil)
		}
	}

	if n.IOSEnabled {
		iosDevices, err := n.listLocalIOSDevices()
		if err != nil {
			log.Printf("list local ios devices: %v", err)
		} else {
			devices = append(devices, iosDevices...)
		}
	}
	if adbErr != nil && len(devices) == 0 {
		return nil, adbErr
	}
	return n.filterBlacklistedDevices(devices), nil
}

func (n *Node) filterBlacklistedDevices(devices []DeviceInfo) []DeviceInfo {
	if len(devices) == 0 {
		return devices
	}
	filtered := devices[:0]
	for _, device := range devices {
		if n.isDeviceBlacklisted(device.Serial) {
			continue
		}
		filtered = append(filtered, device)
	}
	return filtered
}

func (n *Node) listLocalDevices() ([]DeviceInfo, error) {
	devices, err := n.listLocalDeviceStates()
	if err != nil {
		return nil, err
	}

	for i := range devices {
		if devices[i].Platform != PlatformAndroid {
			continue
		}
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

func (n *Node) devicePeerIDs() []string {
	var peerIDs []string
	n.mu.RLock()
	defer n.mu.RUnlock()
	for id, peer := range n.Peers {
		if !peer.AndroidEnabled && !peer.IOSEnabled {
			continue
		}
		peerIDs = append(peerIDs, id)
	}
	slices.Sort(peerIDs)
	return peerIDs
}

type peerDevicesResult struct {
	peerID  string
	devices []DeviceInfo
	err     error
}

func (n *Node) listPeerDevicesConcurrently(ctx context.Context, peerIDs []string) []peerDevicesResult {
	if len(peerIDs) == 0 {
		return nil
	}

	resultsByPeer := make(map[string]peerDevicesResult, len(peerIDs))
	results := make(chan peerDevicesResult, len(peerIDs))
	var wg sync.WaitGroup
	for _, peerID := range peerIDs {
		wg.Add(1)
		go func(peerID string) {
			defer wg.Done()
			devices, err := n.listPeerDevices(ctx, peerID)
			results <- peerDevicesResult{peerID: peerID, devices: devices, err: err}
		}(peerID)
	}
	wg.Wait()
	close(results)

	for result := range results {
		resultsByPeer[result.peerID] = result
	}

	ordered := make([]peerDevicesResult, 0, len(peerIDs))
	for _, peerID := range peerIDs {
		ordered = append(ordered, resultsByPeer[peerID])
	}
	return ordered
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

// Geometry returns the live screenshot and input coordinate spaces for a
// device. Mast intentionally owns the ioslink/WDA session used for iOS.
func (n *Node) Geometry(serial string) (*DeviceGeometry, error) {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}

	inputWidth, inputHeight := 0, 0
	if device.Platform == PlatformIOS {
		stream, err := n.EnsureStream(serial, streamcfg.Options{})
		if err != nil {
			return nil, err
		}
		inputWidth, inputHeight = stream.Width, stream.Height
	}

	data, err := n.Screenshot(serial)
	if err != nil {
		return nil, err
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode screenshot geometry: %w", err)
	}
	if inputWidth <= 0 || inputHeight <= 0 {
		inputWidth, inputHeight = config.Width, config.Height
	}
	orientation := "portrait"
	if config.Width > config.Height {
		orientation = "landscape"
	}
	return &DeviceGeometry{
		Serial:           serial,
		Platform:         device.Platform,
		Orientation:      orientation,
		ScreenshotWidth:  config.Width,
		ScreenshotHeight: config.Height,
		InputWidth:       inputWidth,
		InputHeight:      inputHeight,
	}, nil
}

func (n *Node) localScreenshot(serial string) ([]byte, error) {
	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.State != "device" {
		return nil, fmt.Errorf("device %s is %s", serial, device.State)
	}
	switch device.Platform {
	case PlatformIOS:
		return n.localIOSScreenshot(serial)
	case PlatformAndroid:
		return n.adb.ExecOut(n.ctx, "", serial, "screencap", "-p")
	default:
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
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
	devices, err := n.listLocalDeviceStates()
	if err != nil {
		return nil, err
	}

	index := slices.IndexFunc(devices, func(d DeviceInfo) bool {
		return d.Serial == serial
	})
	if index != -1 {
		return &devices[index], nil
	}

	return n.peerDeviceBySerial(serial, n.devicePeerIDs())
}

func (n *Node) peerDeviceBySerial(serial string, peerIDs []string) (*DeviceInfo, error) {
	if len(peerIDs) == 0 {
		return nil, errors.New("device not found: " + serial)
	}

	ctx := n.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan peerDevicesResult, len(peerIDs))
	for _, peerID := range peerIDs {
		go func(peerID string) {
			devices, err := n.listPeerDevices(ctx, peerID)
			results <- peerDevicesResult{peerID: peerID, devices: devices, err: err}
		}(peerID)
	}

	var peerErrors []error
	for range peerIDs {
		result := <-results
		if result.err != nil {
			log.Printf("find device %s from peer %s: %v", serial, result.peerID, result.err)
			n.setPeerDeviceError(result.peerID, result.err.Error())
			peerErrors = append(peerErrors, result.err)
			continue
		}
		n.setPeerDeviceError(result.peerID, "")

		index := slices.IndexFunc(result.devices, func(d DeviceInfo) bool {
			return d.Serial == serial
		})
		if index != -1 {
			cancel()
			return &result.devices[index], nil
		}
	}

	if len(peerErrors) > 0 {
		return nil, fmt.Errorf("device not found: %s; peer lookup errors: %w", serial, errors.Join(peerErrors...))
	}

	return nil, errors.New("device not found: " + serial)
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
			Serial:   device.Serial,
			Platform: device.Platform,
			State:    device.State,
			NodeID:   device.NodeID,
			Battery:  deviceBatteryPayload(device.Battery),
		})
	}
	return payloads
}

func deviceInfosFromPayload(payloads []transport.DeviceInfoPayload) []DeviceInfo {
	devices := make([]DeviceInfo, 0, len(payloads))
	for _, payload := range payloads {
		devices = append(devices, DeviceInfo{
			Serial:   payload.Serial,
			Platform: payload.Platform,
			State:    payload.State,
			NodeID:   payload.NodeID,
			Battery:  deviceBatteryFromPayload(payload.Battery),
		})
	}
	return devices
}

func applyBatterySnapshot(device *DeviceInfo, snapshot batterySnapshot) {
	if snapshot.BatteryPercent == nil && snapshot.BatteryState == "" {
		device.Battery = nil
		return
	}
	state := snapshot.BatteryState
	if state == "" {
		state = BatteryStateUnknown
	}
	device.Battery = &DeviceBattery{Percent: snapshot.BatteryPercent, State: state}
}

func deviceBatteryPayload(battery *DeviceBattery) *transport.DeviceBatteryPayload {
	if battery == nil {
		return nil
	}
	return &transport.DeviceBatteryPayload{Percent: battery.Percent, State: string(battery.State)}
}

func deviceBatteryFromPayload(payload *transport.DeviceBatteryPayload) *DeviceBattery {
	if payload == nil {
		return nil
	}
	return &DeviceBattery{Percent: payload.Percent, State: BatteryState(payload.State)}
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
			Serial:   strings.TrimSpace(parts[0]),
			Platform: PlatformAndroid,
			State:    strings.TrimSpace(parts[1]),
			NodeID:   nodeID,
		})
	}
	return devices
}
