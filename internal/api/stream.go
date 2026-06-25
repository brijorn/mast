package api

import (
	"encoding/json"
	"net/http"

	"github.com/brijorn/mast/internal/node"
	streamcfg "github.com/brijorn/mast/internal/stream"
)

type startStreamRequest struct {
	Serial  string            `json:"serial"`
	Options streamcfg.Options `json:"options"`
}

type startStreamResponse struct {
	ID        string `json:"id"`
	Serial    string `json:"serial"`
	Host      string `json:"host"`
	LocalPort int    `json:"local_port"`
}

func (s *Server) StartStream(w http.ResponseWriter, r *http.Request) {
	var req startStreamRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	stream, err := s.node.EnsureStream(req.Serial, req.Options)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeStartStreamResponse(w, req.Serial, stream)
}

func (s *Server) StopStream() {
}
func writeStartStreamResponse(w http.ResponseWriter, serial string, stream *node.StreamSession) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(startStreamResponse{
		ID:        stream.ID,
		Host:      stream.Host,
		Serial:    serial,
		LocalPort: stream.LocalPort,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
