package node

import (
	"errors"

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

	return scrcpy.WriteTouch(session.controlConn, touchAction, x, y, session.Width, session.Height)
}

func (n *Node) Touch(serial string, action string, x int, y int) error {
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

	if err := scrcpy.WriteTap(session.controlConn, x, y, session.Width, session.Height); err != nil {
		return err
	}

	return nil
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

	return scrcpy.WriteSwipe(session.controlConn, startX, startY, endX, endY, session.Width, session.Height)
}

func (n *Node) Swipe(serial string, startX, startY, endX, endY int) error {
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
