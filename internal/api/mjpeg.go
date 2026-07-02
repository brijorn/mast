package api

import (
	"errors"
	"log"
	"net/http"
)

func (s *Server) StreamMJPEG(w http.ResponseWriter, r *http.Request) {
	serial := r.URL.Query().Get("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	stream, err := s.node.GetStream(serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if stream.Kind != "mjpeg" {
		http.Error(w, "active stream is not MJPEG", http.StatusBadRequest)
		return
	}

	if err := stream.StreamMJPEG(r.Context(), w); err != nil && !errors.Is(err, r.Context().Err()) {
		log.Printf("mjpeg stream %s: %v", serial, err)
	}
}
