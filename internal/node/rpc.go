package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brijorn/mast/internal/transport"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type peerRPCResponse struct {
	messageType string
	data        []byte
}

type peerRequest struct {
	transport.RawMessage
	Payload any `json:"payload"`
}

func (n *Node) sendPeerRequest(peerID string, messageType string, payload any) error {
	peer, ok := n.GetPeer(peerID)
	if !ok {
		return errors.New("peer not found")
	}

	msg := &peerRequest{
		RawMessage: transport.RawMessage{
			Type:      messageType,
			ID:        uuid.NewString(),
			From:      n.ID,
			To:        peerID,
			Timestamp: time.Now(),
		},
		Payload: payload,
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	if err := peer.WriteMessage(websocket.TextMessage, encoded); err != nil {
		n.dropPeer(peerID, peer)
		return fmt.Errorf("peer %s disconnected: %w", peerID, err)
	}

	return nil
}

func (n *Node) sendPeerRPC(ctx context.Context, peerID string, messageType string, payload any) (peerRPCResponse, error) {
	peer, ok := n.GetPeer(peerID)
	if !ok {
		return peerRPCResponse{}, errors.New("peer not found")
	}

	msgID := uuid.NewString()
	msg := &peerRequest{
		RawMessage: transport.RawMessage{
			Type:      messageType,
			ID:        msgID,
			From:      n.ID,
			To:        peerID,
			Timestamp: time.Now(),
		},
		Payload: payload,
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		return peerRPCResponse{}, err
	}

	responses := make(chan peerRPCResponse, 1)
	n.pendingMu.Lock()
	if n.pending == nil {
		n.pending = make(map[string]chan peerRPCResponse)
	}
	n.pending[msgID] = responses
	n.pendingMu.Unlock()
	defer func() {
		n.pendingMu.Lock()
		delete(n.pending, msgID)
		n.pendingMu.Unlock()
	}()

	if err := peer.WriteMessage(websocket.TextMessage, encoded); err != nil {
		n.dropPeer(peerID, peer)
		return peerRPCResponse{}, fmt.Errorf("peer %s disconnected: %w", peerID, err)
	}

	select {
	case response := <-responses:
		return response, nil
	case <-ctx.Done():
		return peerRPCResponse{}, ctx.Err()
	}
}

func (n *Node) deliverPeerRPCResponse(raw transport.RawMessage, message []byte) bool {
	switch raw.MessageType() {
	case transport.TypeListDevicesResponse,
		transport.TypeDeviceDNSGetResponse,
		transport.TypeDeviceDNSSetResponse,
		transport.TypeDeviceOrientationSetResponse,
		transport.TypeScreenshotResponse,
		transport.TypeStartStreamResponse,
		transport.TypeClipboardGetResponse,
		transport.TypeUpdateCheckResponse,
		transport.TypeUpdateApplyResponse,
		transport.TypeConfigGetResponse,
		transport.TypeConfigUpdateResponse:
	default:
		return false
	}

	n.pendingMu.Lock()
	responses := n.pending[raw.ID]
	n.pendingMu.Unlock()
	if responses == nil {
		return true
	}

	select {
	case responses <- peerRPCResponse{messageType: raw.MessageType(), data: message}:
	default:
	}
	return true
}
