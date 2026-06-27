package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/transport"
)

type RuntimeConfigApplier interface {
	ApplyRuntimeConfig(cfg mastconfig.Config, changedKeys []string) error
}

type RuntimeConfigApplierFunc func(cfg mastconfig.Config, changedKeys []string) error

func (f RuntimeConfigApplierFunc) ApplyRuntimeConfig(cfg mastconfig.Config, changedKeys []string) error {
	return f(cfg, changedKeys)
}

func (n *Node) SetConfig(path string, cfg mastconfig.Config, applier RuntimeConfigApplier) {
	n.configMu.Lock()
	defer n.configMu.Unlock()
	n.configPath = path
	n.configState = cfg.Clone()
	n.configReady = true
	n.configApplier = applier
}

func (n *Node) GetNodeConfig(ctx context.Context, nodeID string) (*mastconfig.Config, error) {
	if nodeID == "" {
		return nil, errors.New("node id required")
	}
	if nodeID == n.ID {
		return n.getLocalConfig()
	}

	response, err := n.sendPeerRPC(ctx, nodeID, transport.TypeConfigGetRequest, nil)
	if err != nil {
		return nil, err
	}
	if response.messageType != transport.TypeConfigGetResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.ConfigGetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, errors.New(res.Payload.Error)
	}
	return &res.Payload.Config, nil
}

func (n *Node) UpdateNodeConfig(ctx context.Context, nodeID string, values map[string]string) (*mastconfig.UpdateResult, error) {
	if nodeID == "" {
		return nil, errors.New("node id required")
	}
	if nodeID == n.ID {
		return n.updateLocalConfig(values)
	}

	payload := transport.ConfigUpdateRequestPayload{Values: values}
	response, err := n.sendPeerRPC(ctx, nodeID, transport.TypeConfigUpdateRequest, payload)
	if err != nil {
		return nil, err
	}
	if response.messageType != transport.TypeConfigUpdateResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.ConfigUpdateResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, errors.New(res.Payload.Error)
	}
	return &res.Payload.Result, nil
}

func (n *Node) getLocalConfig() (*mastconfig.Config, error) {
	n.configMu.RLock()
	defer n.configMu.RUnlock()
	if !n.configReady {
		return nil, errors.New("node config not configured")
	}
	cfg := n.configState.Clone()
	return &cfg, nil
}

func (n *Node) updateLocalConfig(values map[string]string) (*mastconfig.UpdateResult, error) {
	n.configMu.Lock()
	defer n.configMu.Unlock()
	if !n.configReady {
		return nil, errors.New("node config not configured")
	}

	next, changed, restartKeys, err := mastconfig.ApplyValues(n.configState, values)
	if err != nil {
		return nil, err
	}
	if len(changed) > 0 && n.configApplier != nil {
		if err := n.configApplier.ApplyRuntimeConfig(next.Clone(), changed); err != nil {
			return nil, err
		}
	}
	if err := mastconfig.Save(n.configPath, &next); err != nil {
		return nil, err
	}

	n.applyNodeConfig(next)
	n.configState = next.Clone()
	return &mastconfig.UpdateResult{
		Config:              next.Clone(),
		ChangedKeys:         changed,
		RestartRequired:     len(restartKeys) > 0,
		RestartRequiredKeys: restartKeys,
	}, nil
}

func (n *Node) applyNodeConfig(cfg mastconfig.Config) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.AndroidEnabled = cfg.AndroidEnabled
	n.ProxyEnabled = cfg.ProxyEnabled
	n.ADBPort = cfg.ADBPort
	n.AdvertiseHost = cfg.AdvertiseHost
}

func (n *Node) handleConfigGetRequest(peer *PeerConn, req transport.ConfigGetRequest) {
	cfg, err := n.getLocalConfig()
	payload := transport.ConfigGetResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Config = cfg.Clone()
	}
	n.writePeerResponse(peer, transport.TypeConfigGetResponse, req.RawMessage, payload)
}

func (n *Node) handleConfigUpdateRequest(peer *PeerConn, req transport.ConfigUpdateRequest) {
	result, err := n.updateLocalConfig(req.Payload.Values)
	payload := transport.ConfigUpdateResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = *result
	}
	n.writePeerResponse(peer, transport.TypeConfigUpdateResponse, req.RawMessage, payload)
}
