package node

import "github.com/brijorn/mast/internal/version"

type NodeInfo struct {
	ID             string `json:"id"`
	Addr           string `json:"addr,omitempty"`
	Local          bool   `json:"local"`
	AndroidEnabled bool   `json:"android_enabled"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuildDate      string `json:"build_date"`
}

func (n *Node) ListNodes() []NodeInfo {
	nodes := []NodeInfo{
		{
			ID:             n.ID,
			Addr:           n.AdvertiseHost,
			Local:          true,
			AndroidEnabled: n.AndroidEnabled,
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
			Version:        peer.Version,
			Commit:         peer.Commit,
			BuildDate:      peer.BuildDate,
		})
	}

	return nodes
}
