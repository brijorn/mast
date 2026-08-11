package node

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/brijorn/mast/internal/transport"
)

const androidHierarchyPath = "/sdcard/mast-window.xml"

var androidBoundsPattern = regexp.MustCompile(`^\[(\d+),(\d+)\]\[(\d+),(\d+)\]$`)

type DeviceElement struct {
	Type string `json:"type,omitempty"`
	// Identifier is the element's stable name — Android's resource-id, iOS's
	// accessibility name. An automation matches on this rather than on Label,
	// which is a display string and changes with locale.
	Identifier string      `json:"identifier,omitempty"`
	Label      string      `json:"label,omitempty"`
	Value      string      `json:"value,omitempty"`
	Rect       ElementRect `json:"rect"`
	Clickable  bool        `json:"clickable,omitempty"`
	Enabled    bool        `json:"enabled,omitempty"`
}

type ElementRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type androidHierarchyNode struct {
	Class       string                 `xml:"class,attr"`
	ResourceID  string                 `xml:"resource-id,attr"`
	Text        string                 `xml:"text,attr"`
	Description string                 `xml:"content-desc,attr"`
	Bounds      string                 `xml:"bounds,attr"`
	Clickable   bool                   `xml:"clickable,attr"`
	Enabled     bool                   `xml:"enabled,attr"`
	Children    []androidHierarchyNode `xml:"node"`
}

// iosSourceNode is one node of WDA's accessibility tree. Every XCUIElementType
// shares this shape, so the element name is captured rather than enumerated:
// WDA gains element types with iOS releases, and a list would silently drop the
// ones this build had not heard of.
type iosSourceNode struct {
	XMLName  xml.Name
	Type     string          `xml:"type,attr"`
	Name     string          `xml:"name,attr"`
	Label    string          `xml:"label,attr"`
	Value    string          `xml:"value,attr"`
	Enabled  string          `xml:"enabled,attr"`
	Visible  string          `xml:"visible,attr"`
	X        *float64        `xml:"x,attr"`
	Y        *float64        `xml:"y,attr"`
	Width    *float64        `xml:"width,attr"`
	Height   *float64        `xml:"height,attr"`
	Children []iosSourceNode `xml:",any"`
}

type androidHierarchy struct {
	Nodes []androidHierarchyNode `xml:"node"`
}

func (n *Node) Elements(serial string) ([]DeviceElement, error) {
	if serial == "" {
		return nil, errors.New("serial required")
	}
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.Platform != PlatformAndroid && device.Platform != PlatformIOS {
		return nil, fmt.Errorf("element hierarchy is unavailable for platform %s", device.Platform)
	}
	if device.NodeID == n.ID {
		return n.localElements(serial)
	}
	return n.peerElements(n.ctx, device.NodeID, serial)
}

func (n *Node) localElements(serial string) ([]DeviceElement, error) {
	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.Platform == PlatformIOS {
		return n.localIOSElements(serial)
	}
	return n.localAndroidElements(serial)
}

// localIOSElements reads the tree through the device's ioslink session, which
// controlSession opens if the phone is not already streaming.
func (n *Node) localIOSElements(serial string) ([]DeviceElement, error) {
	session, err := n.controlSession(serial)
	if err != nil {
		return nil, err
	}
	if session.iosDevice == nil {
		return nil, errors.New("iOS control connection not available")
	}
	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	document, err := session.iosDevice.Source(ctx)
	if err != nil {
		return nil, fmt.Errorf("read iOS element hierarchy: %w", err)
	}
	return parseIOSElements(document)
}

func parseIOSElements(document string) ([]DeviceElement, error) {
	trimmed := strings.TrimSpace(document)
	if trimmed == "" {
		return nil, errors.New("decode iOS element hierarchy: empty source document")
	}
	var root iosSourceNode
	if err := xml.Unmarshal([]byte(trimmed), &root); err != nil {
		return nil, fmt.Errorf("decode iOS element hierarchy: %w", err)
	}

	elements := make([]DeviceElement, 0)
	var appendNode func(iosSourceNode)
	appendNode = func(node iosSourceNode) {
		if rect, ok := iosNodeRect(node); ok {
			elementType := strings.TrimSpace(node.Type)
			if elementType == "" {
				elementType = strings.TrimSpace(node.XMLName.Local)
			}
			elements = append(elements, DeviceElement{
				Type:       elementType,
				Identifier: strings.TrimSpace(node.Name),
				Label:      strings.TrimSpace(node.Label),
				Value:      strings.TrimSpace(node.Value),
				Rect:       rect,
				// WDA has no clickable attribute. An element that can be acted
				// on is one the accessibility layer is currently showing.
				Clickable: iosAttrBool(node.Visible),
				Enabled:   iosAttrBool(node.Enabled),
			})
		}
		for _, child := range node.Children {
			appendNode(child)
		}
	}
	appendNode(root)
	return elements, nil
}

// iosNodeRect drops a node without a usable rectangle. Reporting it at the
// origin instead would read as a real control in the top-left corner, and get
// tapped.
func iosNodeRect(node iosSourceNode) (ElementRect, bool) {
	if node.X == nil || node.Y == nil || node.Width == nil || node.Height == nil {
		return ElementRect{}, false
	}
	if *node.Width <= 0 || *node.Height <= 0 {
		return ElementRect{}, false
	}
	return ElementRect{X: *node.X, Y: *node.Y, Width: *node.Width, Height: *node.Height}, true
}

func iosAttrBool(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

func (n *Node) localAndroidElements(serial string) ([]DeviceElement, error) {
	if _, err := n.adbShell(n.ctx, "", serial, "uiautomator", "dump", androidHierarchyPath); err != nil {
		return nil, fmt.Errorf("dump Android element hierarchy: %w", err)
	}
	data, err := n.adbShell(n.ctx, "", serial, "cat", androidHierarchyPath)
	if err != nil {
		return nil, fmt.Errorf("read Android element hierarchy: %w", err)
	}
	return parseAndroidElements(data)
}

func parseAndroidElements(data []byte) ([]DeviceElement, error) {
	var hierarchy androidHierarchy
	if err := xml.Unmarshal(data, &hierarchy); err != nil {
		return nil, fmt.Errorf("decode Android element hierarchy: %w", err)
	}
	elements := make([]DeviceElement, 0)
	var appendNode func(androidHierarchyNode)
	appendNode = func(node androidHierarchyNode) {
		if rect, ok := parseAndroidBounds(node.Bounds); ok {
			label := strings.TrimSpace(node.Description)
			if label == "" {
				label = strings.TrimSpace(node.Text)
			}
			elements = append(elements, DeviceElement{
				Type:       node.Class,
				Identifier: strings.TrimSpace(node.ResourceID),
				Label:      label,
				Value:      strings.TrimSpace(node.Text),
				Rect:       rect,
				Clickable:  node.Clickable,
				Enabled:    node.Enabled,
			})
		}
		for _, child := range node.Children {
			appendNode(child)
		}
	}
	for _, node := range hierarchy.Nodes {
		appendNode(node)
	}
	return elements, nil
}

func parseAndroidBounds(value string) (ElementRect, bool) {
	match := androidBoundsPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 5 {
		return ElementRect{}, false
	}
	values := make([]int, 4)
	for index := range values {
		parsed, err := strconv.Atoi(match[index+1])
		if err != nil {
			return ElementRect{}, false
		}
		values[index] = parsed
	}
	if values[2] <= values[0] || values[3] <= values[1] {
		return ElementRect{}, false
	}
	return ElementRect{
		X: float64(values[0]), Y: float64(values[1]),
		Width: float64(values[2] - values[0]), Height: float64(values[3] - values[1]),
	}, true
}

func (n *Node) peerElements(ctx context.Context, peerID, serial string) ([]DeviceElement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, peerDeviceRPCTimeout)
	defer cancel()
	response, err := n.sendPeerRPC(ctx, peerID, transport.TypeElementsRequest, transport.ElementsRequestPayload{Serial: serial})
	if err != nil {
		return nil, fmt.Errorf("elements from peer %s: %w", peerID, err)
	}
	if response.messageType != transport.TypeElementsResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}
	var result transport.ElementsResponse
	if err := json.Unmarshal(response.data, &result); err != nil {
		return nil, err
	}
	if result.Payload.Error != "" {
		return nil, fmt.Errorf("elements from peer %s: %s", peerID, result.Payload.Error)
	}
	elements := make([]DeviceElement, len(result.Payload.Result))
	for index, element := range result.Payload.Result {
		elements[index] = DeviceElement{
			Type: element.Type, Identifier: element.Identifier, Label: element.Label, Value: element.Value,
			Rect:      ElementRect{X: element.X, Y: element.Y, Width: element.Width, Height: element.Height},
			Clickable: element.Clickable, Enabled: element.Enabled,
		}
	}
	return elements, nil
}

func (n *Node) handleElementsRequest(peer *PeerConn, request transport.ElementsRequest) {
	elements, err := n.localElements(request.Payload.Serial)
	payload := transport.ElementsResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = make([]transport.ElementPayload, len(elements))
		for index, element := range elements {
			payload.Result[index] = transport.ElementPayload{
				Type: element.Type, Identifier: element.Identifier, Label: element.Label, Value: element.Value,
				X: element.Rect.X, Y: element.Rect.Y, Width: element.Rect.Width, Height: element.Rect.Height,
				Clickable: element.Clickable, Enabled: element.Enabled,
			}
		}
	}
	n.writePeerResponse(peer, transport.TypeElementsResponse, request.RawMessage, payload)
}
