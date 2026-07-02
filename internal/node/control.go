package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/brijorn/ioslink"
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

const iosTouchMinMoveDistance = 2

func (n *Node) touchLocal(serial string, action string, x int, y int) error {
	session, err := n.GetStream(serial)
	if err != nil {
		return err
	}

	if session.Platform == PlatformIOS {
		return n.touchLocalIOS(session, action, x, y)
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

	if session.Platform == PlatformIOS {
		return n.tapLocalIOS(session, x, y)
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

	if session.Platform == PlatformIOS {
		text, ok := iosTextFromAndroidKeycode(keycode, metaState)
		if !ok {
			return fmt.Errorf("Android keycode %d cannot be translated to iOS text input", keycode)
		}
		return n.typeTextLocalIOS(session, text)
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

func (n *Node) pressButtonLocal(serial string, name string) error {
	session, err := n.GetStream(serial)
	if err != nil {
		return err
	}

	if session.Platform == PlatformIOS {
		return n.pressButtonLocalIOS(session, name)
	}

	keycode, err := androidButtonKeycode(name)
	if err != nil {
		return err
	}
	return n.pressKeyLocal(serial, keycode, 0)
}

func (n *Node) PressButton(serial string, name string) error {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID == n.ID {
		return n.pressButtonLocal(serial, name)
	}

	payload := transport.PressButtonRequestPayload{
		Serial: serial,
		Name:   name,
	}

	return n.sendPeerRequest(device.NodeID, transport.TypePressButtonRequest, payload)
}

func (n *Node) typeTextLocal(serial string, text string) error {
	session, err := n.GetStream(serial)
	if err != nil {
		return err
	}

	if session.Platform == PlatformIOS {
		return n.typeTextLocalIOS(session, text)
	}

	if session.controlConn == nil {
		return errors.New("stream control connection not available")
	}

	session.controlMu.Lock()
	defer session.controlMu.Unlock()

	return scrcpy.WriteText(session.controlConn, text)
}

func (n *Node) TypeText(serial string, text string) error {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID == n.ID {
		return n.typeTextLocal(serial, text)
	}

	payload := transport.TextInputRequestPayload{
		Serial: serial,
		Text:   text,
	}

	return n.sendPeerRequest(device.NodeID, transport.TypeTextInputRequest, payload)
}

func (n *Node) getClipboardLocal(serial string) (string, error) {
	session, err := n.GetStream(serial)
	if err != nil {
		return "", err
	}

	if session.Platform == PlatformIOS {
		return n.getClipboardLocalIOS(session)
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

	if session.Platform == PlatformIOS {
		return n.setClipboardLocalIOS(session, text)
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

	if session.Platform == PlatformIOS {
		return n.swipeLocalIOS(session, startX, startY, endX, endY)
	}

	if session.controlConn == nil {
		return errors.New("stream control connection not available")
	}

	session.controlMu.Lock()
	defer session.controlMu.Unlock()

	return scrcpy.WriteSwipe(session.controlConn, startX, startY, endX, endY, session.Width, session.Height)
}

func (n *Node) touchLocalIOS(session *StreamSession, action string, x int, y int) error {
	if session.iosDevice == nil {
		return errors.New("iOS control connection not available")
	}

	session.iosTouchMu.Lock()
	switch action {
	case "down":
		session.iosTouch = &iosTouchState{
			Points:    []iosTouchPoint{{X: x, Y: y}},
			StartedAt: time.Now(),
		}
		session.iosTouchMu.Unlock()
		return nil
	case "move":
		if session.iosTouch != nil {
			session.iosTouch.Points = appendIOSTouchPoint(session.iosTouch.Points, iosTouchPoint{X: x, Y: y})
		}
		session.iosTouchMu.Unlock()
		return nil
	case "up":
		touch := session.iosTouch
		session.iosTouch = nil
		session.iosTouchMu.Unlock()
		if touch == nil {
			return n.tapLocalIOS(session, x, y)
		}
		points := appendIOSTouchPoint(touch.Points, iosTouchPoint{X: x, Y: y})
		if iosTouchPathDistance(points) < 4 {
			return n.tapLocalIOS(session, x, y)
		}
		return n.dragLocalIOS(session, points, time.Since(touch.StartedAt))
	default:
		session.iosTouchMu.Unlock()
		return errors.New("invalid touch action")
	}
}

func (n *Node) tapLocalIOS(session *StreamSession, x int, y int) error {
	if session.iosDevice == nil {
		return errors.New("iOS control connection not available")
	}
	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	return session.iosDevice.Tap(ctx, float64(x), float64(y))
}

func (n *Node) swipeLocalIOS(session *StreamSession, startX, startY, endX, endY int) error {
	if session.iosDevice == nil {
		return errors.New("iOS control connection not available")
	}
	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	return session.iosDevice.SwipeAsync(ctx, ioslink.SwipeRequest{
		FromX:    float64(startX),
		FromY:    float64(startY),
		ToX:      float64(endX),
		ToY:      float64(endY),
		Duration: 0.2,
	})
}

func (n *Node) dragLocalIOS(session *StreamSession, points []iosTouchPoint, elapsed time.Duration) error {
	if session.iosDevice == nil {
		return errors.New("iOS control connection not available")
	}
	if len(points) == 0 {
		return errors.New("iOS drag path is empty")
	}

	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	return session.iosDevice.DragAsync(ctx, ioslink.DragRequest{
		Points:   ioslinkTouchPoints(points),
		Duration: iosDragDuration(elapsed),
	})
}

func (n *Node) pressButtonLocalIOS(session *StreamSession, name string) error {
	if session.iosDevice == nil {
		return errors.New("iOS control connection not available")
	}
	button, err := iosButtonName(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	return session.iosDevice.PressButton(ctx, button)
}

func (n *Node) typeTextLocalIOS(session *StreamSession, text string) error {
	if session.iosDevice == nil {
		return errors.New("iOS control connection not available")
	}
	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	return session.iosDevice.SendKeys(ctx, text)
}

func (n *Node) getClipboardLocalIOS(session *StreamSession) (string, error) {
	if session.iosDevice == nil {
		return "", errors.New("iOS control connection not available")
	}
	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	return session.iosDevice.GetClipboard(ctx)
}

func (n *Node) setClipboardLocalIOS(session *StreamSession, text string) error {
	if session.iosDevice == nil {
		return errors.New("iOS control connection not available")
	}
	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	clipboardErr := session.iosDevice.SetClipboard(ctx, text)
	if err := n.typeTextLocalIOS(session, text); err != nil {
		if clipboardErr != nil {
			return fmt.Errorf("set iOS clipboard: %v; type text: %w", clipboardErr, err)
		}
		return err
	}
	return nil
}

func appendIOSTouchPoint(points []iosTouchPoint, point iosTouchPoint) []iosTouchPoint {
	if len(points) == 0 {
		return append(points, point)
	}
	last := points[len(points)-1]
	if math.Hypot(float64(point.X-last.X), float64(point.Y-last.Y)) < iosTouchMinMoveDistance {
		return points
	}
	return append(points, point)
}

func iosTouchPathDistance(points []iosTouchPoint) float64 {
	if len(points) < 2 {
		return 0
	}
	start := points[0]
	var maxDistance float64
	for _, point := range points[1:] {
		distance := math.Hypot(float64(point.X-start.X), float64(point.Y-start.Y))
		if distance > maxDistance {
			maxDistance = distance
		}
	}
	return maxDistance
}

func ioslinkTouchPoints(points []iosTouchPoint) []ioslink.TouchPoint {
	out := make([]ioslink.TouchPoint, 0, len(points))
	for _, point := range points {
		out = append(out, ioslink.TouchPoint{
			X: float64(point.X),
			Y: float64(point.Y),
		})
	}
	return out
}

func iosDragDuration(elapsed time.Duration) float64 {
	if elapsed < 200*time.Millisecond {
		return 0.2
	}
	if elapsed > 2*time.Second {
		return 2
	}
	return elapsed.Seconds()
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

func androidButtonKeycode(name string) (uint32, error) {
	switch name {
	case "back":
		return scrcpy.KeycodeBack, nil
	case "home":
		return scrcpy.KeycodeHome, nil
	case "app_switch":
		return scrcpy.KeycodeAppSwitch, nil
	default:
		return 0, errors.New("unsupported Android button: " + name)
	}
}

func iosButtonName(name string) (string, error) {
	switch name {
	case "home":
		return "home", nil
	case "volumeUp":
		return "volumeUp", nil
	case "volumeDown":
		return "volumeDown", nil
	default:
		return "", errors.New("unsupported iOS button: " + name)
	}
}

func iosTextFromAndroidKeycode(keycode uint32, metaState uint32) (string, bool) {
	shifted := metaState&0x0001 != 0

	if keycode >= 29 && keycode <= 54 {
		base := rune('a')
		if shifted {
			base = 'A'
		}
		return string(base + rune(keycode-29)), true
	}

	if keycode >= 7 && keycode <= 16 {
		index := keycode - 7
		if shifted {
			return []string{")", "!", "@", "#", "$", "%", "^", "&", "*", "("}[index], true
		}
		return string(rune('0') + rune(index)), true
	}

	switch keycode {
	case 17:
		return "*", true
	case 18:
		return "#", true
	case 55:
		return shiftedAndroidText(",", "<", shifted), true
	case 56:
		return shiftedAndroidText(".", ">", shifted), true
	case 61:
		return "\t", true
	case 62:
		return " ", true
	case 66:
		return "\n", true
	case 67:
		return "\b", true
	case 68:
		return shiftedAndroidText("`", "~", shifted), true
	case 69:
		return shiftedAndroidText("-", "_", shifted), true
	case 70:
		return shiftedAndroidText("=", "+", shifted), true
	case 71:
		return shiftedAndroidText("[", "{", shifted), true
	case 72:
		return shiftedAndroidText("]", "}", shifted), true
	case 73:
		return shiftedAndroidText("\\", "|", shifted), true
	case 74:
		return shiftedAndroidText(";", ":", shifted), true
	case 75:
		return shiftedAndroidText("'", "\"", shifted), true
	case 76:
		return shiftedAndroidText("/", "?", shifted), true
	case 77:
		return "@", true
	case 81:
		return "+", true
	default:
		return "", false
	}
}

func shiftedAndroidText(normal string, shifted string, isShifted bool) string {
	if isShifted {
		return shifted
	}
	return normal
}
