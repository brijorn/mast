package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brijorn/mast/internal/transport"
)

const (
	androidDNSAutomaticMode = "opportunistic"
	androidDNSOffMode       = "off"
	androidDNSHostnameMode  = "hostname"
)

type DeviceDNSMode string

const (
	DeviceDNSModeOff       DeviceDNSMode = "off"
	DeviceDNSModeAutomatic DeviceDNSMode = "automatic"
	DeviceDNSModeHostname  DeviceDNSMode = "hostname"
	DeviceDNSModeUnknown   DeviceDNSMode = "unknown"
)

type DeviceDNSStatus struct {
	Mode     DeviceDNSMode `json:"mode"`
	Hostname string        `json:"hostname,omitempty"`
}

func (n *Node) DeviceDNS(serial string) (*DeviceDNSStatus, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.NodeID == n.ID {
		return n.localDeviceDNS(serial)
	}
	return n.peerDeviceDNS(n.ctx, device.NodeID, serial)
}

func (n *Node) SetDeviceDNS(serial string, desired DeviceDNSStatus) (*DeviceDNSStatus, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.Platform != PlatformAndroid {
		if device.Platform == PlatformIOS {
			return nil, errors.New("private DNS is not supported for iOS devices")
		}
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
	if device.NodeID == n.ID {
		return n.setLocalDeviceDNS(serial, desired)
	}
	return n.setPeerDeviceDNS(n.ctx, device.NodeID, serial, desired)
}

func (n *Node) localDeviceDNS(serial string) (*DeviceDNSStatus, error) {
	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	switch device.Platform {
	case PlatformIOS:
		return nil, errors.New("private DNS is not supported for iOS devices")
	case PlatformAndroid:
	default:
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
	if device.State != "device" {
		return nil, fmt.Errorf("device %s is %s", serial, device.State)
	}

	modeOutput, err := n.adb.Shell(n.ctx, "", serial, "settings", "get", "global", "private_dns_mode")
	if err != nil {
		return nil, err
	}
	hostnameOutput, err := n.adb.Shell(n.ctx, "", serial, "settings", "get", "global", "private_dns_specifier")
	if err != nil {
		return nil, err
	}

	return deviceDNSStatus(strings.TrimSpace(string(modeOutput)), strings.TrimSpace(string(hostnameOutput))), nil
}

func (n *Node) setLocalDeviceDNS(serial string, desired DeviceDNSStatus) (*DeviceDNSStatus, error) {
	if err := n.requireLocalReadyDevice(serial); err != nil {
		return nil, err
	}

	switch desired.Mode {
	case DeviceDNSModeHostname:
		hostname := strings.TrimSpace(desired.Hostname)
		if hostname == "" {
			return nil, errors.New("hostname required for hostname DNS mode")
		}
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "global", "private_dns_mode", androidDNSHostnameMode); err != nil {
			return nil, err
		}
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "global", "private_dns_specifier", hostname); err != nil {
			return nil, err
		}
	case DeviceDNSModeAutomatic:
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "global", "private_dns_mode", androidDNSAutomaticMode); err != nil {
			return nil, err
		}
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "delete", "global", "private_dns_specifier"); err != nil {
			return nil, err
		}
	case DeviceDNSModeOff:
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "global", "private_dns_mode", androidDNSOffMode); err != nil {
			return nil, err
		}
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "delete", "global", "private_dns_specifier"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported DNS mode %q", desired.Mode)
	}

	return n.localDeviceDNS(serial)
}

func (n *Node) requireLocalReadyDevice(serial string) error {
	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return err
	}
	if device.State != "device" {
		return fmt.Errorf("device %s is %s", serial, device.State)
	}
	return nil
}

func deviceDNSStatus(mode string, hostname string) *DeviceDNSStatus {
	if hostname == "null" {
		hostname = ""
	}
	var normalized DeviceDNSMode
	switch mode {
	case "", "null", androidDNSAutomaticMode:
		normalized = DeviceDNSModeAutomatic
	case androidDNSOffMode:
		normalized = DeviceDNSModeOff
	case androidDNSHostnameMode:
		normalized = DeviceDNSModeHostname
	default:
		normalized = DeviceDNSModeUnknown
	}
	return &DeviceDNSStatus{
		Mode:     normalized,
		Hostname: hostname,
	}
}

func (n *Node) peerDeviceDNS(ctx context.Context, peerID string, serial string) (*DeviceDNSStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.DeviceDNSGetRequestPayload{Serial: serial}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeDeviceDNSGetRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("device dns from peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeDeviceDNSGetResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DeviceDNSGetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("device dns from peer %s: %s", peerID, res.Payload.Error)
	}
	return deviceDNSStatusFromPayload(res.Payload.Result), nil
}

func (n *Node) setPeerDeviceDNS(ctx context.Context, peerID string, serial string, desired DeviceDNSStatus) (*DeviceDNSStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.DeviceDNSSetRequestPayload{Serial: serial, Mode: string(desired.Mode), Hostname: desired.Hostname}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeDeviceDNSSetRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("set device dns on peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeDeviceDNSSetResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DeviceDNSSetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("set device dns on peer %s: %s", peerID, res.Payload.Error)
	}
	return deviceDNSStatusFromPayload(res.Payload.Result), nil
}

func (n *Node) handleDeviceDNSGetRequest(peer *PeerConn, req transport.DeviceDNSGetRequest) {
	status, err := n.localDeviceDNS(req.Payload.Serial)
	payload := transport.DeviceDNSGetResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = deviceDNSStatusPayload(status)
	}

	n.writePeerResponse(peer, transport.TypeDeviceDNSGetResponse, req.RawMessage, payload)
}

func (n *Node) handleDeviceDNSSetRequest(peer *PeerConn, req transport.DeviceDNSSetRequest) {
	status, err := n.setLocalDeviceDNS(req.Payload.Serial, DeviceDNSStatus{
		Mode: DeviceDNSMode(req.Payload.Mode), Hostname: req.Payload.Hostname,
	})
	payload := transport.DeviceDNSSetResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = deviceDNSStatusPayload(status)
	}

	n.writePeerResponse(peer, transport.TypeDeviceDNSSetResponse, req.RawMessage, payload)
}

func deviceDNSStatusPayload(status *DeviceDNSStatus) *transport.DeviceDNSStatusPayload {
	if status == nil {
		return nil
	}
	return &transport.DeviceDNSStatusPayload{
		Mode:     string(status.Mode),
		Hostname: status.Hostname,
	}
}

func deviceDNSStatusFromPayload(payload *transport.DeviceDNSStatusPayload) *DeviceDNSStatus {
	if payload == nil {
		return nil
	}
	return &DeviceDNSStatus{
		Mode:     DeviceDNSMode(payload.Mode),
		Hostname: payload.Hostname,
	}
}
