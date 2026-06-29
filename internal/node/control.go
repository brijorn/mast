package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brijorn/mast/internal/scrcpy"
	"github.com/brijorn/mast/internal/transport"
)

type TapCommand struct {
	Serial string
	X      int
	Y      int
}

type SwipeCommand struct {
	Serial string
	StartX int
	StartY int
	EndX   int
	EndY   int
}

func (n *Node) touchLocal(serial string, action string, x int, y int) error {
	session, err := n.GetStream(serial)
	if err != nil {
		return err
	}

	if session.controlConn == nil {
		return errors.New("stream control connection not available")
	}

	touchAction, err := touchActionByte(action)
	if err != nil {
		return err
	}

	session.controlMu.Lock()
	defer session.controlMu.Unlock()

	return scrcpy.WriteTouch(session.controlConn, touchAction, x, y, session.Width, session.Height)
}

func (n *Node) Touch(serial string, action string, x int, y int) error {
	if session, err := n.GetStream(serial); err == nil && session.controlConn != nil {
		return n.touchLocal(serial, action, x, y)
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID == n.ID {
		return n.touchLocal(serial, action, x, y)
	}

	payload := transport.TouchRequestPayload{
		Serial: serial,
		Action: action,
		X:      x,
		Y:      y,
	}

	return n.sendPeerRequest(device.NodeID, transport.TypeTouchRequest, payload)
}

func (n *Node) tapLocal(serial string, x int, y int) error {
	session, err := n.GetStream(serial)
	if err != nil {
		return err
	}

	if session.controlConn == nil {
		return errors.New("stream control connection not available")
	}

	session.controlMu.Lock()
	defer session.controlMu.Unlock()

	if err := scrcpy.WriteTap(session.controlConn, x, y, session.Width, session.Height); err != nil {
		return err
	}

	return nil
}

func (n *Node) pressKeyLocal(serial string, keycode uint32, metaState uint32) error {
	session, err := n.GetStream(serial)
	if err != nil {
		return err
	}

	if session.controlConn == nil {
		return errors.New("stream control connection not available")
	}

	session.controlMu.Lock()
	defer session.controlMu.Unlock()

	return scrcpy.PressKey(session.controlConn, keycode, metaState)
}
func (n *Node) PressKey(serial string, keycode uint32, metaState uint32) error {
	device, err := n.deviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID == n.ID {
		return n.pressKeyLocal(serial, keycode, metaState)
	}

	payload := transport.PressKeyRequestPayload{
		Serial:    serial,
		Keycode:   keycode,
		MetaState: metaState,
	}

	return n.sendPeerRequest(device.NodeID, transport.TypePressKeyRequest, payload)
}

func (n *Node) getClipboardLocal(serial string) (string, error) {
	session, err := n.GetStream(serial)
	if err != nil {
		return "", err
	}

	if session.controlConn == nil {
		return "", errors.New("stream control connection not available")
	}

	session.controlMu.Lock()
	defer session.controlMu.Unlock()

	if err := session.controlConn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return "", err
	}
	defer func() {
		_ = session.controlConn.SetDeadline(time.Time{})
	}()

	if err := scrcpy.WriteGetClipboard(session.controlConn, scrcpy.CopyKeyCopy); err != nil {
		return "", err
	}

	return scrcpy.ReadClipboardMessage(session.controlConn)
}

func (n *Node) GetClipboard(serial string) (string, error) {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return "", err
	}

	if device.NodeID == n.ID {
		return n.getClipboardLocal(serial)
	}

	return n.getPeerClipboard(n.ctx, device.NodeID, serial)
}

func (n *Node) getPeerClipboard(ctx context.Context, peerID string, serial string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()

	payload := transport.ClipboardGetRequestPayload{Serial: serial}
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeClipboardGetRequest, payload)
	if err != nil {
		return "", fmt.Errorf("clipboard from peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeClipboardGetResponse {
		return "", fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.ClipboardGetResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return "", err
	}
	if res.Payload.Error != "" {
		return "", fmt.Errorf("clipboard from peer %s: %s", peerID, res.Payload.Error)
	}
	return res.Payload.Text, nil
}

func (n *Node) handleClipboardGetRequest(peer *PeerConn, req transport.ClipboardGetRequest) {
	text, err := n.getClipboardLocal(req.Payload.Serial)
	payload := transport.ClipboardGetResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Text = text
	}

	n.writePeerResponse(peer, transport.TypeClipboardGetResponse, req.RawMessage, payload)
}

func (n *Node) setClipboardLocal(serial string, text string) error {
	session, err := n.GetStream(serial)
	if err != nil {
		return err
	}

	if session.controlConn == nil {
		return errors.New("stream control connection not available")
	}

	session.controlMu.Lock()
	defer session.controlMu.Unlock()

	return scrcpy.WriteSetClipboard(session.controlConn, uint64(time.Now().UnixNano()), text, true)
}

func (n *Node) SetClipboard(serial string, text string) error {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID == n.ID {
		return n.setClipboardLocal(serial, text)
	}

	payload := transport.ClipboardSetRequestPayload{
		Serial: serial,
		Text:   text,
	}
	return n.sendPeerRequest(device.NodeID, transport.TypeClipboardSetRequest, payload)
}

func (n *Node) Tap(serial string, x int, y int) error {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID == n.ID {
		return n.tapLocal(serial, x, y)
	}

	payload := transport.TapRequestPayload{
		Serial: serial,
		X:      x,
		Y:      y,
	}

	return n.sendPeerRequest(device.NodeID, transport.TypeTapRequest, payload)
}

func (n *Node) swipeLocal(serial string, startX, startY, endX, endY int) error {
	session, err := n.GetStream(serial)
	if err != nil {
		return err
	}

	if session.controlConn == nil {
		return errors.New("stream control connection not available")
	}

	session.controlMu.Lock()
	defer session.controlMu.Unlock()

	return scrcpy.WriteSwipe(session.controlConn, startX, startY, endX, endY, session.Width, session.Height)
}

func (n *Node) Swipe(serial string, startX, startY, endX, endY int) error {
	if session, err := n.GetStream(serial); err == nil && session.controlConn != nil {
		return n.swipeLocal(serial, startX, startY, endX, endY)
	}

	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID == n.ID {
		return n.swipeLocal(serial, startX, startY, endX, endY)
	}

	payload := transport.SwipeRequestPayload{
		Serial: serial,
		StartX: startX,
		StartY: startY,
		EndX:   endX,
		EndY:   endY,
	}

	return n.sendPeerRequest(device.NodeID, transport.TypeSwipeRequest, payload)
}

func touchActionByte(action string) (byte, error) {
	switch action {
	case "down":
		return scrcpy.ActionDown, nil
	case "move":
		return scrcpy.ActionMove, nil
	case "up":
		return scrcpy.ActionUp, nil
	default:
		return 0, errors.New("invalid touch action")
	}
}
