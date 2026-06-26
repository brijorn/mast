package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brijorn/mast/internal/transport"
	"github.com/brijorn/mast/internal/update"
	"github.com/gorilla/websocket"
)

func (n *Node) CheckNodeUpdate(ctx context.Context, nodeID string) (*update.CheckResult, error) {
	if nodeID == "" {
		return nil, errors.New("node id required")
	}
	if nodeID == n.ID {
		return n.checkLocalUpdate(ctx)
	}

	response, err := n.sendPeerRPC(ctx, nodeID, transport.TypeUpdateCheckRequest, nil)
	if err != nil {
		return nil, err
	}
	if response.messageType != transport.TypeUpdateCheckResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.UpdateCheckResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, errors.New(res.Payload.Error)
	}
	if res.Payload.Result == nil {
		return nil, errors.New("update check response missing result")
	}

	return checkResultFromPayload(res.Payload.Result), nil
}

func (n *Node) ApplyNodeUpdate(ctx context.Context, nodeID string, opts update.ApplyOptions) (*update.ApplyResult, error) {
	if nodeID == "" {
		return nil, errors.New("node id required")
	}
	if nodeID == n.ID {
		return n.applyLocalUpdate(ctx, opts)
	}

	payload := transport.UpdateApplyOptionsPayload{Force: opts.Force}
	response, err := n.sendPeerRPC(ctx, nodeID, transport.TypeUpdateApplyRequest, payload)
	if err != nil {
		return nil, err
	}
	if response.messageType != transport.TypeUpdateApplyResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.UpdateApplyResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, errors.New(res.Payload.Error)
	}
	if res.Payload.Result == nil {
		return nil, errors.New("update apply response missing result")
	}

	return applyResultFromPayload(res.Payload.Result), nil
}

func (n *Node) checkLocalUpdate(ctx context.Context) (*update.CheckResult, error) {
	checker := n.updateChecker
	if checker == nil {
		checker = &update.Checker{}
	}
	return checker.Check(ctx)
}

func (n *Node) applyLocalUpdate(ctx context.Context, opts update.ApplyOptions) (*update.ApplyResult, error) {
	applier := n.updateApplier
	if applier == nil {
		applier = &update.Applier{Checker: n.updateChecker}
	}
	return applier.Apply(ctx, opts)
}

func (n *Node) handleUpdateCheckRequest(peer *PeerConn, req transport.UpdateCheckRequest) {
	result, err := n.checkLocalUpdate(n.ctx)
	payload := transport.UpdateCheckResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = checkResultPayload(result)
	}

	n.writePeerResponse(peer, transport.TypeUpdateCheckResponse, req.RawMessage, payload)
}

func (n *Node) handleUpdateApplyRequest(peer *PeerConn, req transport.UpdateApplyRequest) {
	opts := update.ApplyOptions{Force: req.Payload.Force}
	result, err := n.applyLocalUpdate(n.ctx, opts)
	payload := transport.UpdateApplyResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = applyResultPayload(result)
	}

	n.writePeerResponse(peer, transport.TypeUpdateApplyResponse, req.RawMessage, payload)
}

func (n *Node) writePeerResponse(peer *PeerConn, messageType string, req transport.RawMessage, payload any) {
	msg := peerRequest{
		RawMessage: transport.RawMessage{
			Type:      messageType,
			ID:        req.ID,
			From:      n.ID,
			To:        req.From,
			Timestamp: time.Now(),
		},
		Payload: payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = peer.WriteMessage(websocket.TextMessage, data)
}

func checkResultPayload(result *update.CheckResult) *transport.UpdateCheckResultPayload {
	if result == nil {
		return nil
	}
	return &transport.UpdateCheckResultPayload{
		CurrentVersion:  result.CurrentVersion,
		LatestVersion:   result.LatestVersion,
		UpdateAvailable: result.UpdateAvailable,
		OS:              result.OS,
		Arch:            result.Arch,
		AssetName:       result.AssetName,
		AssetURL:        result.AssetURL,
		ChecksumURL:     result.ChecksumURL,
	}
}

func checkResultFromPayload(payload *transport.UpdateCheckResultPayload) *update.CheckResult {
	if payload == nil {
		return nil
	}
	return &update.CheckResult{
		CurrentVersion:  payload.CurrentVersion,
		LatestVersion:   payload.LatestVersion,
		UpdateAvailable: payload.UpdateAvailable,
		OS:              payload.OS,
		Arch:            payload.Arch,
		AssetName:       payload.AssetName,
		AssetURL:        payload.AssetURL,
		ChecksumURL:     payload.ChecksumURL,
	}
}

func applyResultPayload(result *update.ApplyResult) *transport.UpdateApplyResultPayload {
	if result == nil {
		return nil
	}
	return &transport.UpdateApplyResultPayload{
		CurrentVersion:  result.CurrentVersion,
		LatestVersion:   result.LatestVersion,
		Updated:         result.Updated,
		RestartRequired: result.RestartRequired,
		Message:         result.Message,
	}
}

func applyResultFromPayload(payload *transport.UpdateApplyResultPayload) *update.ApplyResult {
	if payload == nil {
		return nil
	}
	return &update.ApplyResult{
		CurrentVersion:  payload.CurrentVersion,
		LatestVersion:   payload.LatestVersion,
		Updated:         payload.Updated,
		RestartRequired: payload.RestartRequired,
		Message:         payload.Message,
	}
}
