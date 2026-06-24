package node

import (
	"errors"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

type DeviceInfo struct {
	Serial string `json:"serial"`
	State  string `json:"state"`
	NodeID string `json:"node_id"`
}

type adbRunner interface {
	Devices(host string) ([]byte, error)
	Push(host string, localPath string, remotePath string) error
	Reverse(host string, deviceSocket string, localPort int) error
	StartShell(host string, arg ...string) (*exec.Cmd, error)
}

type realADB struct{}

func (a realADB) run(host string, arg ...string) ([]byte, error) {
	var args []string

	if host != "" {
		args = append(args, "-H", host, "-P", "5037")
	}
	args = append(args, arg...)
	return exec.Command("adb", args...).Output()
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
	var args []string

	if host != "" {
		args = append(args, "-H", host, "-P", "5037")
	}

	args = append(args, "shell")
	args = append(args, arg...)
	cmd := exec.Command("adb", args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
func (n *Node) ListDevices() ([]DeviceInfo, error) {
	var devices []DeviceInfo

	rawOutput, err := n.adb.Devices("")
	if err != nil {
		return nil, err
	}

	devices = parseDevicesOutput(string(rawOutput), n.ID, devices)

	for id, peer := range n.Peers {
		if !peer.AndroidEnabled {
			continue
		}

		rawOutput, err := n.adb.Devices(peer.Addr)
		if err != nil {
			return nil, err
		}

		devices = parseDevicesOutput(string(rawOutput), id, devices)

	}

	return devices, nil
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
