package transport

import (
	"time"

	streamcfg "github.com/brijorn/mast/internal/stream"
)

const (
	TypeConnectionRequest  = "connection_request"
	TypeStartStreamRequest = "start_stream_request"
	TypeStopStreamRequest  = "stop_stream_request"
	TypeTouchRequest       = "touch_request"
	TypeTapRequest         = "tap_request"
	TypeSwipeRequest       = "swipe_request"
)

type Message interface {
	MessageType() string
	MessageID() string
}

type RawMessage struct {
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Timestamp time.Time `json:"timestamp"`
}

func (msg *RawMessage) MessageType() string {
	return msg.Type
}

func (msg *RawMessage) MessageID() string {
	return msg.ID
}

type Heartbeat struct {
	RawMessage
}

type ConnectionRequestPayload struct {
	AndroidEnabled bool `json:"android_enabled"`
}

type ConnectionRequest struct {
	RawMessage
	Payload ConnectionRequestPayload `json:"payload"`
}

type StartStreamRequestPayload struct {
	Serial  string            `json:"serial"`
	Options streamcfg.Options `json:"options"`
}
type StartStreamRequest struct {
	RawMessage
	Payload StartStreamRequestPayload `json:"payload"`
}

type StopStreamRequestPayload struct {
	Serial string `json:"serial"`
}

type StopStreamRequest struct {
	RawMessage
	Payload StopStreamRequestPayload `json:"payload"`
}

type TapRequest struct {
	RawMessage
	Payload TapRequestPayload `json:"payload"`
}

type TapRequestPayload struct {
	Serial string `json:"serial"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
}

type TouchRequest struct {
	RawMessage
	Payload TouchRequestPayload `json:"payload"`
}

type TouchRequestPayload struct {
	Serial string `json:"serial"`
	Action string `json:"action"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
}

type SwipeRequest struct {
	RawMessage
	Payload SwipeRequestPayload `json:"payload"`
}

type SwipeRequestPayload struct {
	Serial string `json:"serial"`
	StartX int    `json:"start_x"`
	StartY int    `json:"start_y"`
	EndX   int    `json:"end_x"`
	EndY   int    `json:"end_y"`
}
