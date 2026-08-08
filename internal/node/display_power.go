package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brijorn/mast/internal/transport"
)

// Operator control of one phone's physical panel.
//
// The panel is not the same thing as the Android device. Mast darkens a screen
// with scrcpy's SET_DISPLAY_POWER, which drives the display hardware and leaves
// the power manager untouched: a dark phone still reports itself awake and
// interactive, adb screenshots still return pixels, and injected input still
// lands. Anything reaching for KEYCODE_SLEEP or KEYCODE_POWER to work the panel
// is therefore addressing the wrong layer — those move the power manager and
// cannot lift a panel this session is holding down.
//
// So the control has to live here, beside the policy that competes with it.
// `keep_display_off` is the node's steady state and re-asserts every thirty
// seconds; an override recorded against a single serial wins over it until the
// operator clears it or the device reconnects. Without that arrangement, "turn
// this screen on" would last until the next tick of the policy and no longer.
type DeviceDisplayPower string

const (
	// DeviceDisplayPowerOn and DeviceDisplayPowerOff are operator overrides.
	DeviceDisplayPowerOn  DeviceDisplayPower = "on"
	DeviceDisplayPowerOff DeviceDisplayPower = "off"
	// DeviceDisplayPowerPolicy clears the override and returns the device to
	// whatever the node's `keep_display_off` config says.
	DeviceDisplayPowerPolicy DeviceDisplayPower = "policy"
	// DeviceDisplayPowerUnknown is the panel state of a device Mast is not
	// holding: no policy, no override, nothing asserted. Reporting it honestly
	// matters more than guessing "on", because a caller that has to label a
	// button can then say it does not know instead of being wrong.
	DeviceDisplayPowerUnknown DeviceDisplayPower = "unknown"
)

// DeviceDisplayPowerStatus separates the three questions a caller conflates at
// its peril: what the operator asked for, what Mast last successfully told the
// panel, and what the node would do left alone.
type DeviceDisplayPowerStatus struct {
	Serial   string `json:"serial"`
	Platform string `json:"platform"`
	// Requested is the override in force: on, off, or policy for none.
	Requested DeviceDisplayPower `json:"requested"`
	// Panel is the last display power Mast asserted and the device accepted,
	// or unknown when Mast holds no session for this device.
	Panel DeviceDisplayPower `json:"panel"`
	// Policy is what the node's config alone would hold the panel at.
	Policy DeviceDisplayPower `json:"policy"`
}

func (n *Node) DeviceDisplayPower(serial string) (*DeviceDisplayPowerStatus, error) {
	device, err := n.displayPowerDevice(serial)
	if err != nil {
		return nil, err
	}
	if device.NodeID == n.ID {
		return n.localDeviceDisplayPower(serial)
	}
	return n.peerDeviceDisplayPower(n.ctx, device.NodeID, serial)
}

func (n *Node) SetDeviceDisplayPower(
	serial string,
	requested DeviceDisplayPower,
) (*DeviceDisplayPowerStatus, error) {
	if err := validateDeviceDisplayPower(requested); err != nil {
		return nil, err
	}
	device, err := n.displayPowerDevice(serial)
	if err != nil {
		return nil, err
	}
	if device.NodeID == n.ID {
		return n.setLocalDeviceDisplayPower(serial, requested)
	}
	return n.setPeerDeviceDisplayPower(n.ctx, device.NodeID, serial, requested)
}

func (n *Node) displayPowerDevice(serial string) (*DeviceInfo, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.Platform != PlatformAndroid {
		if device.Platform == PlatformIOS {
			return nil, errors.New("display power control is not supported for iOS devices")
		}
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
	return device, nil
}

func validateDeviceDisplayPower(requested DeviceDisplayPower) error {
	switch requested {
	case DeviceDisplayPowerOn, DeviceDisplayPowerOff, DeviceDisplayPowerPolicy:
		return nil
	default:
		return fmt.Errorf("unsupported display power %q", requested)
	}
}

func (n *Node) localDeviceDisplayPower(serial string) (*DeviceDisplayPowerStatus, error) {
	if err := n.requireLocalReadyDevice(serial); err != nil {
		return nil, err
	}
	return n.deviceDisplayPowerStatus(serial), nil
}

func (n *Node) setLocalDeviceDisplayPower(
	serial string,
	requested DeviceDisplayPower,
) (*DeviceDisplayPowerStatus, error) {
	if err := validateDeviceDisplayPower(requested); err != nil {
		return nil, err
	}
	if err := n.requireLocalReadyDevice(serial); err != nil {
		return nil, err
	}
	if managed, _ := n.devicePowerConfig(); !managed {
		return nil, fmt.Errorf("node %s does not manage Android device power", n.ID)
	}

	n.devicePowerMu.Lock()
	if n.devicePowerOverride == nil {
		n.devicePowerOverride = make(map[string]bool)
	}
	if requested == DeviceDisplayPowerPolicy {
		delete(n.devicePowerOverride, serial)
	} else {
		n.devicePowerOverride[serial] = requested == DeviceDisplayPowerOn
	}
	session := n.devicePowerSessions[serial]
	n.devicePowerMu.Unlock()

	hold, wantOn := n.devicePowerIntent(serial)
	switch {
	case !hold:
		// Nothing should be holding this panel any more. Drop the session so the
		// device is left as the phone itself decides, and report it unknown
		// rather than claiming the last value Mast happened to write.
		n.stopUnwantedDevicePowerSessions()
	case session != nil:
		if err := n.applyDevicePower(serial, session, wantOn); err != nil {
			n.discardDevicePowerSession(serial, session)
			return nil, fmt.Errorf("set display power on %s: %w", serial, err)
		}
	default:
		// No session yet — the node runs no display-off policy, or the last one
		// died. Build it synchronously so the caller learns whether the panel
		// actually moved instead of being told yes and waiting for a tick.
		if err := n.startDeviceDisplayPowerNow(serial, wantOn); err != nil {
			return nil, err
		}
	}

	n.requestDevicePowerPolicy()
	return n.deviceDisplayPowerStatus(serial), nil
}

// startDeviceDisplayPowerNow brings up a control session and writes the wanted
// power to it, so an operator's click is answered by the device rather than by
// the reconcile loop that would have got round to it within thirty seconds.
func (n *Node) startDeviceDisplayPowerNow(serial string, on bool) error {
	attempt := newDevicePowerAttempt(n.ctx)
	n.devicePowerMu.Lock()
	if n.devicePowerStarting[serial] != nil {
		n.devicePowerMu.Unlock()
		attempt.release()
		return fmt.Errorf("display power session for %s is already starting", serial)
	}
	if n.devicePowerStarting == nil {
		n.devicePowerStarting = make(map[string]*devicePowerAttempt)
	}
	n.devicePowerStarting[serial] = attempt
	n.devicePowerMu.Unlock()

	session, err := n.startDevicePowerSession(attempt, serial)

	n.devicePowerMu.Lock()
	if n.devicePowerStarting[serial] == attempt {
		delete(n.devicePowerStarting, serial)
	}
	keep := err == nil && n.devicePowerReady[serial] && n.devicePowerSessions[serial] == nil
	if keep {
		n.devicePowerSessions[serial] = session
		delete(n.devicePowerFailures, serial)
	}
	n.devicePowerMu.Unlock()
	attempt.release()

	if err != nil {
		return fmt.Errorf("start display power session for %s: %w", serial, err)
	}
	if !keep {
		_ = session.stop()
		return fmt.Errorf("display power session for %s was superseded", serial)
	}

	go n.watchDevicePowerSession(serial, session)
	if err := n.applyDevicePower(serial, session, on); err != nil {
		n.discardDevicePowerSession(serial, session)
		return fmt.Errorf("set display power on %s: %w", serial, err)
	}
	return nil
}

func (n *Node) deviceDisplayPowerStatus(serial string) *DeviceDisplayPowerStatus {
	_, keepDisplayOff := n.devicePowerConfig()

	n.devicePowerMu.Lock()
	override, overridden := n.devicePowerOverride[serial]
	asserted, held := n.devicePowerAsserted[serial]
	n.devicePowerMu.Unlock()

	status := &DeviceDisplayPowerStatus{
		Serial:    serial,
		Platform:  PlatformAndroid,
		Requested: DeviceDisplayPowerPolicy,
		Panel:     DeviceDisplayPowerUnknown,
		Policy:    DeviceDisplayPowerUnknown,
	}
	if overridden {
		status.Requested = displayPowerFromBool(override)
	}
	if held {
		status.Panel = displayPowerFromBool(asserted)
	}
	if keepDisplayOff {
		status.Policy = DeviceDisplayPowerOff
	}
	return status
}

func displayPowerFromBool(on bool) DeviceDisplayPower {
	if on {
		return DeviceDisplayPowerOn
	}
	return DeviceDisplayPowerOff
}

func (n *Node) peerDeviceDisplayPower(
	ctx context.Context,
	peerID string,
	serial string,
) (*DeviceDisplayPowerStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.DisplayPowerGetRequestPayload{Serial: serial}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeDisplayPowerGetRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("read display power on peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeDisplayPowerGetResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DisplayPowerGetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("read display power on peer %s: %s", peerID, res.Payload.Error)
	}
	return deviceDisplayPowerStatusFromPayload(res.Payload.Result), nil
}

func (n *Node) setPeerDeviceDisplayPower(
	ctx context.Context,
	peerID string,
	serial string,
	requested DeviceDisplayPower,
) (*DeviceDisplayPowerStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.DisplayPowerSetRequestPayload{
		Serial:    serial,
		Requested: string(requested),
	}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeDisplayPowerSetRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("set display power on peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeDisplayPowerSetResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DisplayPowerSetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("set display power on peer %s: %s", peerID, res.Payload.Error)
	}
	return deviceDisplayPowerStatusFromPayload(res.Payload.Result), nil
}

func (n *Node) handleDisplayPowerGetRequest(peer *PeerConn, req transport.DisplayPowerGetRequest) {
	status, err := n.localDeviceDisplayPower(req.Payload.Serial)
	payload := transport.DisplayPowerGetResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = deviceDisplayPowerStatusPayload(status)
	}
	n.writePeerResponse(peer, transport.TypeDisplayPowerGetResponse, req.RawMessage, payload)
}

func (n *Node) handleDisplayPowerSetRequest(peer *PeerConn, req transport.DisplayPowerSetRequest) {
	status, err := n.setLocalDeviceDisplayPower(
		req.Payload.Serial,
		DeviceDisplayPower(req.Payload.Requested),
	)
	payload := transport.DisplayPowerSetResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = deviceDisplayPowerStatusPayload(status)
	}
	n.writePeerResponse(peer, transport.TypeDisplayPowerSetResponse, req.RawMessage, payload)
}

func deviceDisplayPowerStatusPayload(status *DeviceDisplayPowerStatus) *transport.DisplayPowerStatusPayload {
	if status == nil {
		return nil
	}
	return &transport.DisplayPowerStatusPayload{
		Serial:    status.Serial,
		Platform:  status.Platform,
		Requested: string(status.Requested),
		Panel:     string(status.Panel),
		Policy:    string(status.Policy),
	}
}

func deviceDisplayPowerStatusFromPayload(
	payload *transport.DisplayPowerStatusPayload,
) *DeviceDisplayPowerStatus {
	if payload == nil {
		return nil
	}
	return &DeviceDisplayPowerStatus{
		Serial:    payload.Serial,
		Platform:  payload.Platform,
		Requested: DeviceDisplayPower(payload.Requested),
		Panel:     DeviceDisplayPower(payload.Panel),
		Policy:    DeviceDisplayPower(payload.Policy),
	}
}
