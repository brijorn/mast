package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brijorn/mast/internal/transport"
)

type DeviceOrientation string

const (
	DeviceOrientationPortrait  DeviceOrientation = "portrait"
	DeviceOrientationLandscape DeviceOrientation = "landscape"
)

type DeviceOrientationStatus struct {
	Serial      string            `json:"serial"`
	Platform    string            `json:"platform"`
	Orientation DeviceOrientation `json:"orientation"`
}

func (n *Node) SetDeviceOrientation(serial string, orientation DeviceOrientation) (*DeviceOrientationStatus, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}
	if orientation != DeviceOrientationPortrait && orientation != DeviceOrientationLandscape {
		return nil, fmt.Errorf("unsupported device orientation %q", orientation)
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.Platform != PlatformAndroid {
		if device.Platform == PlatformIOS {
			return nil, errors.New("device orientation control is not supported for iOS devices")
		}
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
	if device.NodeID == n.ID {
		return n.setLocalDeviceOrientation(serial, orientation)
	}
	return n.setPeerDeviceOrientation(n.ctx, device.NodeID, serial, orientation)
}

func (n *Node) setLocalDeviceOrientation(serial string, orientation DeviceOrientation) (*DeviceOrientationStatus, error) {
	if orientation != DeviceOrientationPortrait && orientation != DeviceOrientationLandscape {
		return nil, fmt.Errorf("unsupported device orientation %q", orientation)
	}
	if err := n.requireLocalReadyDevice(serial); err != nil {
		return nil, err
	}

	rotation := "0"
	if orientation == DeviceOrientationLandscape {
		rotation = "1"
	}
	commands := [][]string{
		{"wm", "set-ignore-orientation-request", "-d", "0", "true"},
		{"settings", "put", "system", "accelerometer_rotation", "0"},
		{"settings", "put", "system", "user_rotation", rotation},
	}
	for _, command := range commands {
		if _, err := n.adb.Shell(n.ctx, "", serial, command...); err != nil {
			return nil, fmt.Errorf("set %s orientation on %s: %w", orientation, serial, err)
		}
	}

	return &DeviceOrientationStatus{
		Serial:      serial,
		Platform:    PlatformAndroid,
		Orientation: orientation,
	}, nil
}

func (n *Node) setPeerDeviceOrientation(
	ctx context.Context,
	peerID string,
	serial string,
	orientation DeviceOrientation,
) (*DeviceOrientationStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.DeviceOrientationSetRequestPayload{
		Serial:      serial,
		Orientation: string(orientation),
	}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeDeviceOrientationSetRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("set device orientation on peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeDeviceOrientationSetResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DeviceOrientationSetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("set device orientation on peer %s: %s", peerID, res.Payload.Error)
	}
	return deviceOrientationStatusFromPayload(res.Payload.Result), nil
}

func (n *Node) handleDeviceOrientationSetRequest(peer *PeerConn, req transport.DeviceOrientationSetRequest) {
	status, err := n.setLocalDeviceOrientation(
		req.Payload.Serial,
		DeviceOrientation(req.Payload.Orientation),
	)
	payload := transport.DeviceOrientationSetResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = deviceOrientationStatusPayload(status)
	}

	n.writePeerResponse(peer, transport.TypeDeviceOrientationSetResponse, req.RawMessage, payload)
}

func deviceOrientationStatusPayload(status *DeviceOrientationStatus) *transport.DeviceOrientationStatusPayload {
	if status == nil {
		return nil
	}
	return &transport.DeviceOrientationStatusPayload{
		Serial:      status.Serial,
		Platform:    status.Platform,
		Orientation: string(status.Orientation),
	}
}

func deviceOrientationStatusFromPayload(payload *transport.DeviceOrientationStatusPayload) *DeviceOrientationStatus {
	if payload == nil {
		return nil
	}
	return &DeviceOrientationStatus{
		Serial:      payload.Serial,
		Platform:    payload.Platform,
		Orientation: DeviceOrientation(payload.Orientation),
	}
}
