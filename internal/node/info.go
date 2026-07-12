package node

import "github.com/brijorn/mast/internal/version"

type NodeInfo struct {
	ID             string `json:"id"`
	Addr           string `json:"addr,omitempty"`
	Local          bool   `json:"local"`
	AndroidEnabled bool   `json:"android_enabled"`
	IOSEnabled     bool   `json:"ios_enabled"`
	ProxyEnabled   bool   `json:"proxy_enabled"`
	ADBPort        int    `json:"adb_port,omitempty"`
	APIAddr        string `json:"api_addr,omitempty"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuildDate      string `json:"build_date"`
	DeviceError    string `json:"device_error,omitempty"`
}

func (n *Node) ListNodes() []NodeInfo {
	nodes := []NodeInfo{
		{
			ID:             n.ID,
			Addr:           n.AdvertiseHost,
			Local:          true,
			AndroidEnabled: n.AndroidEnabled,
			IOSEnabled:     n.IOSEnabled,
			ProxyEnabled:   n.ProxyEnabled,
			ADBPort:        n.ADBPort,
			APIAddr:        n.APIAddr,
			Version:        version.Version,
			Commit:         version.Commit,
			BuildDate:      version.Date,
		},
	}

	n.mu.RLock()
	defer n.mu.RUnlock()
	for id, peer := range n.Peers {
		nodes = append(nodes, NodeInfo{
			ID:             id,
			Addr:           peer.Addr,
			Local:          false,
			AndroidEnabled: peer.AndroidEnabled,
			IOSEnabled:     peer.IOSEnabled,
			ProxyEnabled:   peer.ProxyEnabled,
			ADBPort:        peer.ADBPort,
			APIAddr:        peer.APIAddr,
			Version:        peer.Version,
			Commit:         peer.Commit,
			BuildDate:      peer.BuildDate,
			DeviceError:    peer.DeviceError,
		})
	}

	return nodes
}

func (n *Node) setPeerDeviceError(peerID string, message string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if peer, ok := n.Peers[peerID]; ok {
		peer.DeviceError = message
	}
}
