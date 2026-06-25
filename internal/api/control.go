package api

import (
	"encoding/json"
	"net/http"
)

type tapRequest struct {
	Serial string `json:"serial"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
}

type touchRequest struct {
	Serial string `json:"serial"`
	Action string `json:"action"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
}

type swipeRequest struct {
	Serial string `json:"serial"`
	StartX int    `json:"start_x"`
	StartY int    `json:"start_y"`
	EndX   int    `json:"end_x"`
	EndY   int    `json:"end_y"`
}

func (s *Server) Touch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	if err := s.node.Touch(req.Serial, req.Action, req.X, req.Y); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Tap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

func (s *Server) Swipe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
