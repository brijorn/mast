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
	Platform  string `json:"platform"`
	Kind      string `json:"kind"`
	Host      string `json:"host"`
	LocalPort int    `json:"local_port"`
	VideoURL  string `json:"video_url,omitempty"`
	MJPEGURL  string `json:"mjpeg_url,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
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
	req.Options = req.Options.WithDefaults()

	stream, err := s.node.EnsureStream(req.Serial, req.Options)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeStartStreamResponse(w, req.Serial, stream)
}

func (s *Server) StopStream(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}
	if err := s.node.StopStream(serial); err != nil {
		if node.IsStreamNotFound(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func writeStartStreamResponse(w http.ResponseWriter, serial string, stream *node.StreamSession) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(startStreamResponse{
		ID:        stream.ID,
		Platform:  stream.Platform,
		Kind:      stream.Kind,
		Host:      stream.Host,
		Serial:    serial,
		LocalPort: stream.LocalPort,
		VideoURL:  stream.VideoURL,
		MJPEGURL:  stream.MJPEGURL,
		Width:     stream.Width,
		Height:    stream.Height,
	}); err != nil {
		return
	}
}
