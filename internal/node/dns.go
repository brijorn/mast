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
	deviceDNSAutomaticMode = "opportunistic"
	deviceDNSHostnameMode  = "hostname"
	deviceDNSAdGuardHost   = "dns.adguard.com"
)

type DeviceDNSStatus struct {
	Mode      string `json:"mode"`
	Hostname  string `json:"hostname,omitempty"`
	Automatic bool   `json:"automatic"`
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

func (n *Node) ToggleDeviceDNS(serial string) (*DeviceDNSStatus, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.NodeID == n.ID {
		return n.toggleLocalDeviceDNS(serial)
	}
	return n.togglePeerDeviceDNS(n.ctx, device.NodeID, serial)
}

func (n *Node) localDeviceDNS(serial string) (*DeviceDNSStatus, error) {
	if err := n.requireLocalReadyDevice(serial); err != nil {
		return nil, err
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

func (n *Node) toggleLocalDeviceDNS(serial string) (*DeviceDNSStatus, error) {
	status, err := n.localDeviceDNS(serial)
	if err != nil {
		return nil, err
	}

	if status.Automatic {
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "global", "private_dns_mode", deviceDNSHostnameMode); err != nil {
			return nil, err
		}
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "global", "private_dns_specifier", deviceDNSAdGuardHost); err != nil {
			return nil, err
		}
	} else {
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "global", "private_dns_mode", deviceDNSAutomaticMode); err != nil {
			return nil, err
		}
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "delete", "global", "private_dns_specifier"); err != nil {
			return nil, err
		}
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
	if mode == "" || mode == "null" {
		mode = deviceDNSAutomaticMode
	}
	if hostname == "null" {
		hostname = ""
	}
	return &DeviceDNSStatus{
		Mode:      mode,
		Hostname:  hostname,
		Automatic: mode == deviceDNSAutomaticMode,
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

func (n *Node) togglePeerDeviceDNS(ctx context.Context, peerID string, serial string) (*DeviceDNSStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.DeviceDNSToggleRequestPayload{Serial: serial}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeDeviceDNSToggleRequest, payload)
	if err != nil {
		return nil, fmt.Errorf("toggle device dns from peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeDeviceDNSToggleResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.DeviceDNSToggleResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, fmt.Errorf("toggle device dns from peer %s: %s", peerID, res.Payload.Error)
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

func (n *Node) handleDeviceDNSToggleRequest(peer *PeerConn, req transport.DeviceDNSToggleRequest) {
	status, err := n.toggleLocalDeviceDNS(req.Payload.Serial)
	payload := transport.DeviceDNSToggleResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = deviceDNSStatusPayload(status)
	}

	n.writePeerResponse(peer, transport.TypeDeviceDNSToggleResponse, req.RawMessage, payload)
}

func deviceDNSStatusPayload(status *DeviceDNSStatus) *transport.DeviceDNSStatusPayload {
	if status == nil {
		return nil
	}
	return &transport.DeviceDNSStatusPayload{
		Mode:      status.Mode,
		Hostname:  status.Hostname,
		Automatic: status.Automatic,
	}
}

func deviceDNSStatusFromPayload(payload *transport.DeviceDNSStatusPayload) *DeviceDNSStatus {
	if payload == nil {
		return nil
	}
	return &DeviceDNSStatus{
		Mode:      payload.Mode,
		Hostname:  payload.Hostname,
		Automatic: payload.Automatic,
	}
}
