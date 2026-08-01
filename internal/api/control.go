package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/brijorn/mast/internal/scrcpy"
	"github.com/gorilla/websocket"
)

const (
	controlWSWriteWait  = 5 * time.Second
	controlWSPongWait   = 60 * time.Second
	controlWSPingPeriod = 25 * time.Second
	controlWSQueueSize  = 256
)

var controlUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type tapRequest struct {
	Serial string `json:"serial"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
}

type launchAppRequest struct {
	Serial  string `json:"serial"`
	Package string `json:"package"`
}

// Package names reach an adb shell argument, so only plain Android
// application-id characters are accepted.
var launchPackagePattern = regexp.MustCompile(`^[A-Za-z0-9._]+$`)

type openURLRequest struct {
	Serial string `json:"serial"`
	URL    string `json:"url"`
}

type devToolsRequest struct {
	Serial string `json:"serial"`
	Port   int    `json:"port,omitempty"`
}

type devToolsResponse struct {
	Serial string `json:"serial"`
	Port   int    `json:"port"`
}

// The URL is single-quoted into a device shell command, so a single quote
// would end that quoting; control characters and whitespace cannot appear in a
// URL at all. Only https is accepted: a payout page is not worth opening over
// a transport that can be rewritten in flight.
var openURLDisallowedPattern = regexp.MustCompile(`['\s\x00-\x1f]`)

func validOpenURL(raw string) bool {
	if raw == "" || len(raw) > 2000 {
		return false
	}
	if openURLDisallowedPattern.MatchString(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" && parsed.Host != ""
}

type touchRequest struct {
	Serial    string  `json:"serial"`
	Action    string  `json:"action"`
	X         int     `json:"x"`
	Y         int     `json:"y"`
	PointerID *uint64 `json:"pointer_id,omitempty"`
}

type controlWSRequest struct {
	Type      string  `json:"type"`
	Action    string  `json:"action,omitempty"`
	X         int     `json:"x,omitempty"`
	Y         int     `json:"y,omitempty"`
	StartX    int     `json:"start_x,omitempty"`
	StartY    int     `json:"start_y,omitempty"`
	EndX      int     `json:"end_x,omitempty"`
	EndY      int     `json:"end_y,omitempty"`
	PointerID *uint64 `json:"pointer_id,omitempty"`
}

type controlWSErrorResponse struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type swipeRequest struct {
	Serial string `json:"serial"`
	StartX int    `json:"start_x"`
	StartY int    `json:"start_y"`
	EndX   int    `json:"end_x"`
	EndY   int    `json:"end_y"`
}

type PressKeyRequest struct {
	Serial    string `json:"serial"`
	Keycode   uint32 `json:"keycode"`
	MetaState uint32 `json:"meta_state,omitempty"`
}

type pressButtonRequest struct {
	Serial string `json:"serial"`
	Name   string `json:"name"`
}

type textInputRequest struct {
	Serial string `json:"serial"`
	Text   string `json:"text"`
}

type clipboardRequest struct {
	Serial string `json:"serial"`
	Text   string `json:"text"`
}

type clipboardResponse struct {
	Text string `json:"text"`
}

func (s *Server) Touch(w http.ResponseWriter, r *http.Request) {
	var req touchRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if req.Action != "down" && req.Action != "move" && req.Action != "up" {
		http.Error(w, "action must be down, move, or up", http.StatusBadRequest)
		return
	}

	if req.X < 0 || req.Y < 0 {
		http.Error(w, "non-negative x, y required", http.StatusBadRequest)
		return
	}

	var err error
	if req.PointerID == nil {
		err = s.node.Touch(req.Serial, req.Action, req.X, req.Y)
	} else {
		err = s.node.TouchPointer(req.Serial, req.Action, req.X, req.Y, *req.PointerID)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ControlWebSocket(w http.ResponseWriter, r *http.Request) {
	serial := r.URL.Query().Get("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	conn, err := controlUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	var writeMu sync.Mutex
	if err := conn.SetReadDeadline(time.Now().Add(controlWSPongWait)); err != nil {
		return
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(controlWSPongWait))
	})

	done := make(chan struct{})
	defer close(done)
	requests := make(chan controlWSRequest, controlWSQueueSize)
	defer close(requests)
	go func() {
		ticker := time.NewTicker(controlWSPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(controlWSWriteWait))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
				if err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	go func() {
		for req := range requests {
			s.handleControlWSRequest(conn, &writeMu, serial, req)
		}
	}()

	for {
		var req controlWSRequest
		if err := conn.ReadJSON(&req); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				_ = writeControlWSError(conn, &writeMu, "invalid control message")
			}
			return
		}

		if message := validateControlWSRequest(req); message != "" {
			_ = writeControlWSError(conn, &writeMu, message)
			continue
		}

		select {
		case requests <- req:
		default:
			_ = writeControlWSError(conn, &writeMu, "control queue full")
		}
	}
}

func (s *Server) handleControlWSRequest(conn *websocket.Conn, writeMu *sync.Mutex, serial string, req controlWSRequest) {
	var err error
	switch req.Type {
	case "touch":
		if req.PointerID == nil {
			err = s.node.Touch(serial, req.Action, req.X, req.Y)
		} else {
			err = s.node.TouchPointer(serial, req.Action, req.X, req.Y, *req.PointerID)
		}
	case "swipe":
		err = s.node.Swipe(serial, req.StartX, req.StartY, req.EndX, req.EndY)
	}
	if err != nil {
		_ = writeControlWSError(conn, writeMu, err.Error())
	}
}

func validateControlWSRequest(req controlWSRequest) string {
	switch req.Type {
	case "touch":
		if req.Action != "down" && req.Action != "move" && req.Action != "up" {
			return "action must be down, move, or up"
		}
		if req.X < 0 || req.Y < 0 {
			return "non-negative x, y required"
		}
	case "swipe":
		if req.StartX < 0 || req.StartY < 0 || req.EndX < 0 || req.EndY < 0 {
			return "non-negative coordinates required"
		}
	default:
		return "type must be touch or swipe"
	}

	return ""
}

func writeControlWSError(conn *websocket.Conn, writeMu *sync.Mutex, message string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := conn.SetWriteDeadline(time.Now().Add(controlWSWriteWait)); err != nil {
		return err
	}
	return conn.WriteJSON(controlWSErrorResponse{
		Type:    "error",
		Message: message,
	})
}

func (s *Server) GetClipboard(w http.ResponseWriter, r *http.Request) {
	var req clipboardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	text, err := s.node.GetClipboard(req.Serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(clipboardResponse{Text: text})
}

func (s *Server) SetClipboard(w http.ResponseWriter, r *http.Request) {
	var req clipboardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if err := s.node.SetClipboard(req.Serial, req.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Tap(w http.ResponseWriter, r *http.Request) {
	var req tapRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if req.X < 0 || req.Y < 0 {
		http.Error(w, "non-negative x, y required", http.StatusBadRequest)
		return
	}

	if err := s.node.Tap(req.Serial, req.X, req.Y); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) LaunchApp(w http.ResponseWriter, r *http.Request) {
	var req launchAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if req.Package == "" || !launchPackagePattern.MatchString(req.Package) {
		http.Error(w, "valid package required", http.StatusBadRequest)
		return
	}

	if err := s.node.LaunchApp(req.Serial, req.Package); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) OpenURL(w http.ResponseWriter, r *http.Request) {
	var req openURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if !validOpenURL(req.URL) {
		http.Error(w, "valid https url required", http.StatusBadRequest)
		return
	}

	if err := s.node.OpenURL(req.Serial, req.URL); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DevToolsForward(w http.ResponseWriter, r *http.Request) {
	var req devToolsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	port, err := s.node.DevToolsForward(req.Serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(devToolsResponse{Serial: req.Serial, Port: port}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) DevToolsForwardRemove(w http.ResponseWriter, r *http.Request) {
	var req devToolsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if req.Port <= 0 {
		http.Error(w, "port required", http.StatusBadRequest)
		return
	}

	if err := s.node.DevToolsForwardRemove(req.Serial, req.Port); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Swipe(w http.ResponseWriter, r *http.Request) {
	var req swipeRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if req.StartX < 0 || req.StartY < 0 || req.EndX < 0 || req.EndY < 0 {
		http.Error(w, "non-negative coordinates required", http.StatusBadRequest)
		return
	}

	if err := s.node.Swipe(req.Serial, req.StartX, req.StartY, req.EndX, req.EndY); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) PressKey(w http.ResponseWriter, r *http.Request) {
	var req PressKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if req.Keycode == 0 {
		http.Error(w, "keycode required", http.StatusBadRequest)
		return
	}

	if !scrcpy.ValidKeycodes[int(req.Keycode)] {
		http.Error(w, "invalid keycode", http.StatusBadRequest)
		return
	}

	if err := s.node.PressKey(req.Serial, req.Keycode, req.MetaState); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) PressButton(w http.ResponseWriter, r *http.Request) {
	var req pressButtonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "button name required", http.StatusBadRequest)
		return
	}

	if err := s.node.PressButton(req.Serial, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) TypeText(w http.ResponseWriter, r *http.Request) {
	var req textInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	if err := s.node.TypeText(req.Serial, req.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
