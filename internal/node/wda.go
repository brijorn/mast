package node

import (
	"context"
	"errors"
	"fmt"

	"encoding/json"

	"github.com/brijorn/ioslink"
	"github.com/brijorn/mast/internal/transport"
)

// This file proxies WebDriverAgent's element and source operations to a locally
// owned iOS device. A program driving iOS Settings or closing an ad navigates
// through WDA's xpath tree, not through simple taps, and these are the calls it
// makes. They are local-only: a program runs on the node that owns its phone,
// so it reaches this node's Mast API directly and never crosses a peer link.

// iosController returns the ioslink session for a locally owned iOS device,
// opening one through controlSession if the phone is not already streaming.
func (n *Node) iosController(serial string) (iosElementController, error) {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.Platform != PlatformIOS {
		return nil, fmt.Errorf("WDA element operations are unavailable for platform %s", device.Platform)
	}
	if device.NodeID != n.ID {
		return nil, errors.New("WDA element operations are local-only; call the node that owns the device")
	}
	session, err := n.controlSession(serial)
	if err != nil {
		return nil, err
	}
	if session.iosDevice == nil {
		return nil, errors.New("iOS control connection not available")
	}
	return session.iosDevice, nil
}

// iosElementController is the subset of ioslink.Controller these proxies use.
type iosElementController interface {
	Source(context.Context) (string, error)
	FindElement(context.Context, string, string) (string, error)
	FindElements(context.Context, string, string) ([]string, error)
	ClickElement(context.Context, string) error
	ClearElement(context.Context, string) error
	SetElementValue(context.Context, string, string) error
	ElementRect(context.Context, string) (ioslink.Rect, error)
	ElementAttribute(context.Context, string, string) (string, error)
}

func (n *Node) iosCall(serial string, fn func(context.Context, iosElementController) error) error {
	controller, err := n.iosController(serial)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	return fn(ctx, controller)
}

// DeviceSource returns the WDA accessibility source XML for a local iOS device.
func (n *Node) localDeviceSource(serial string) (string, error) {
	var source string
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		s, err := c.Source(ctx)
		source = s
		return err
	})
	return source, err
}

func (n *Node) localFindElement(serial, using, value string) (string, error) {
	var id string
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		found, err := c.FindElement(ctx, using, value)
		id = found
		return err
	})
	return id, err
}

func (n *Node) localFindElements(serial, using, value string) ([]string, error) {
	var ids []string
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		found, err := c.FindElements(ctx, using, value)
		ids = found
		return err
	})
	return ids, err
}

func (n *Node) localClickElement(serial, id string) error {
	return n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		return c.ClickElement(ctx, id)
	})
}

func (n *Node) localClearElement(serial, id string) error {
	return n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		return c.ClearElement(ctx, id)
	})
}

func (n *Node) localSetElementValue(serial, id, value string) error {
	return n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		return c.SetElementValue(ctx, id, value)
	})
}

func (n *Node) localElementRect(serial, id string) (ElementBounds, error) {
	var rect ElementBounds
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		r, err := c.ElementRect(ctx, id)
		rect = ElementBounds{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
		return err
	})
	return rect, err
}

func (n *Node) localElementAttribute(serial, id, name string) (string, error) {
	var value string
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		v, err := c.ElementAttribute(ctx, id, name)
		value = v
		return err
	})
	return value, err
}

// ElementBounds is the rectangle WDA reports for an element, in logical points.
type ElementBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// deviceOwner returns the node id that owns the device, and whether it is local.
func (n *Node) deviceOwner(serial string) (string, bool, error) {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return "", false, err
	}
	if device.Platform != PlatformIOS {
		return "", false, fmt.Errorf("WDA element operations are unavailable for platform %s", device.Platform)
	}
	return device.NodeID, device.NodeID == n.ID, nil
}

// Routed public methods: run locally when this node owns the device, otherwise
// forward to the owning peer.

func (n *Node) DeviceSource(serial string) (string, error) {
	owner, local, err := n.deviceOwner(serial)
	if err != nil {
		return "", err
	}
	if local {
		return n.localDeviceSource(serial)
	}
	resp, err := n.forwardWDA(owner, transport.WDARequestPayload{Op: "source", Serial: serial})
	return resp.Source, err
}

func (n *Node) FindElement(serial, using, value string) (string, error) {
	owner, local, err := n.deviceOwner(serial)
	if err != nil {
		return "", err
	}
	if local {
		return n.localFindElement(serial, using, value)
	}
	resp, err := n.forwardWDA(owner, transport.WDARequestPayload{Op: "find_element", Serial: serial, Using: using, Value: value})
	return resp.ID, err
}

func (n *Node) FindElements(serial, using, value string) ([]string, error) {
	owner, local, err := n.deviceOwner(serial)
	if err != nil {
		return nil, err
	}
	if local {
		return n.localFindElements(serial, using, value)
	}
	resp, err := n.forwardWDA(owner, transport.WDARequestPayload{Op: "find_elements", Serial: serial, Using: using, Value: value})
	return resp.IDs, err
}

func (n *Node) ClickElement(serial, id string) error {
	owner, local, err := n.deviceOwner(serial)
	if err != nil {
		return err
	}
	if local {
		return n.localClickElement(serial, id)
	}
	_, err = n.forwardWDA(owner, transport.WDARequestPayload{Op: "click_element", Serial: serial, ID: id})
	return err
}

func (n *Node) ClearElement(serial, id string) error {
	owner, local, err := n.deviceOwner(serial)
	if err != nil {
		return err
	}
	if local {
		return n.localClearElement(serial, id)
	}
	_, err = n.forwardWDA(owner, transport.WDARequestPayload{Op: "clear_element", Serial: serial, ID: id})
	return err
}

func (n *Node) SetElementValue(serial, id, value string) error {
	owner, local, err := n.deviceOwner(serial)
	if err != nil {
		return err
	}
	if local {
		return n.localSetElementValue(serial, id, value)
	}
	_, err = n.forwardWDA(owner, transport.WDARequestPayload{Op: "set_element_value", Serial: serial, ID: id, Text: value})
	return err
}

func (n *Node) ElementRect(serial, id string) (ElementBounds, error) {
	owner, local, err := n.deviceOwner(serial)
	if err != nil {
		return ElementBounds{}, err
	}
	if local {
		return n.localElementRect(serial, id)
	}
	resp, err := n.forwardWDA(owner, transport.WDARequestPayload{Op: "element_rect", Serial: serial, ID: id})
	if err != nil {
		return ElementBounds{}, err
	}
	if resp.Rect == nil {
		return ElementBounds{}, nil
	}
	return ElementBounds{X: resp.Rect.X, Y: resp.Rect.Y, Width: resp.Rect.Width, Height: resp.Rect.Height}, nil
}

func (n *Node) ElementAttribute(serial, id, name string) (string, error) {
	owner, local, err := n.deviceOwner(serial)
	if err != nil {
		return "", err
	}
	if local {
		return n.localElementAttribute(serial, id, name)
	}
	resp, err := n.forwardWDA(owner, transport.WDARequestPayload{Op: "element_attribute", Serial: serial, ID: id, Name: name})
	return resp.Value, err
}

// forwardWDA sends one WDA operation to the owning peer and returns its result.
func (n *Node) forwardWDA(peerID string, req transport.WDARequestPayload) (transport.WDAResponsePayload, error) {
	ctx, cancel := context.WithTimeout(n.ctx, peerDeviceRPCTimeout)
	defer cancel()
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeWDARequest, req)
	if err != nil {
		return transport.WDAResponsePayload{}, fmt.Errorf("wda %s from peer %s: %w", req.Op, peerID, err)
	}
	if response.messageType != transport.TypeWDAResponse {
		return transport.WDAResponsePayload{}, fmt.Errorf("unexpected response type: %s", response.messageType)
	}
	var result transport.WDAResponse
	if err := json.Unmarshal(response.data, &result); err != nil {
		return transport.WDAResponsePayload{}, err
	}
	if result.Payload.Error != "" {
		return transport.WDAResponsePayload{}, fmt.Errorf("wda %s from peer %s: %s", req.Op, peerID, result.Payload.Error)
	}
	return result.Payload, nil
}

// handleWDARequest runs one forwarded WDA operation locally and replies.
func (n *Node) handleWDARequest(peer *PeerConn, request transport.WDARequest) {
	p := request.Payload
	out := transport.WDAResponsePayload{}
	var err error
	switch p.Op {
	case "source":
		out.Source, err = n.localDeviceSource(p.Serial)
	case "find_element":
		out.ID, err = n.localFindElement(p.Serial, p.Using, p.Value)
	case "find_elements":
		out.IDs, err = n.localFindElements(p.Serial, p.Using, p.Value)
	case "click_element":
		err = n.localClickElement(p.Serial, p.ID)
	case "clear_element":
		err = n.localClearElement(p.Serial, p.ID)
	case "set_element_value":
		err = n.localSetElementValue(p.Serial, p.ID, p.Text)
	case "element_rect":
		var rect ElementBounds
		rect, err = n.localElementRect(p.Serial, p.ID)
		if err == nil {
			out.Rect = &transport.WDARectValue{X: rect.X, Y: rect.Y, Width: rect.Width, Height: rect.Height}
		}
	case "element_attribute":
		out.Value, err = n.localElementAttribute(p.Serial, p.ID, p.Name)
	default:
		err = fmt.Errorf("unknown WDA op %q", p.Op)
	}
	if err != nil {
		out.Error = err.Error()
	}
	n.writePeerResponse(peer, transport.TypeWDAResponse, request.RawMessage, out)
}
