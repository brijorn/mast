package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brijorn/mast/internal/transport"
)

type DeviceInfo struct {
	Serial string `json:"serial"`
	State  string `json:"state"`
	NodeID string `json:"node_id"`
}

const peerDeviceRPCTimeout = 10 * time.Second

type adbRunner interface {
	Devices(host string) ([]byte, error)
	Push(host string, localPath string, remotePath string) error
	Reverse(host string, deviceSocket string, localPort int) error
	StartShell(host string, arg ...string) (*exec.Cmd, error)
}

type realADB struct{}

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

func (n *Node) listLocalDevices() ([]DeviceInfo, error) {
	rawOutput, err := n.adb.Devices("")
	if err != nil {
		return nil, err
	}

	return parseDevicesOutput(string(rawOutput), n.ID, nil), nil
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
		return nil, err
	}
	if response.messageType != transport.TypeListDevicesResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.ListDevicesResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, errors.New(res.Payload.Error)
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

func deviceInfoPayloads(devices []DeviceInfo) []transport.DeviceInfoPayload {
	payloads := make([]transport.DeviceInfoPayload, 0, len(devices))
	for _, device := range devices {
		payloads = append(payloads, transport.DeviceInfoPayload{
			Serial: device.Serial,
			State:  device.State,
			NodeID: device.NodeID,
		})
	}
	return payloads
}

func deviceInfosFromPayload(payloads []transport.DeviceInfoPayload) []DeviceInfo {
	devices := make([]DeviceInfo, 0, len(payloads))
	for _, payload := range payloads {
		devices = append(devices, DeviceInfo{
			Serial: payload.Serial,
			State:  payload.State,
			NodeID: payload.NodeID,
		})
	}
	return devices
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
			Serial: parts[0],
			State:  parts[1],
			NodeID: nodeID,
		})
	}
	return devices
}
