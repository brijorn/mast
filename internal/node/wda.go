package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/brijorn/ioslink"
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
func (n *Node) DeviceSource(serial string) (string, error) {
	var source string
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		s, err := c.Source(ctx)
		source = s
		return err
	})
	return source, err
}

func (n *Node) FindElement(serial, using, value string) (string, error) {
	var id string
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		found, err := c.FindElement(ctx, using, value)
		id = found
		return err
	})
	return id, err
}

func (n *Node) FindElements(serial, using, value string) ([]string, error) {
	var ids []string
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		found, err := c.FindElements(ctx, using, value)
		ids = found
		return err
	})
	return ids, err
}

func (n *Node) ClickElement(serial, id string) error {
	return n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		return c.ClickElement(ctx, id)
	})
}

func (n *Node) ClearElement(serial, id string) error {
	return n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		return c.ClearElement(ctx, id)
	})
}

func (n *Node) SetElementValue(serial, id, value string) error {
	return n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		return c.SetElementValue(ctx, id, value)
	})
}

func (n *Node) ElementRect(serial, id string) (ElementBounds, error) {
	var rect ElementBounds
	err := n.iosCall(serial, func(ctx context.Context, c iosElementController) error {
		r, err := c.ElementRect(ctx, id)
		rect = ElementBounds{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
		return err
	})
	return rect, err
}

func (n *Node) ElementAttribute(serial, id, name string) (string, error) {
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
