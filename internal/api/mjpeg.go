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

	tracked := &trackingResponseWriter{ResponseWriter: w}
	if err := s.node.StreamMJPEG(r.Context(), serial, tracked); err != nil && !errors.Is(err, r.Context().Err()) {
		log.Printf("mjpeg stream %s: %v", serial, err)
		if !tracked.wrote {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

type trackingResponseWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *trackingResponseWriter) WriteHeader(statusCode int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *trackingResponseWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}

func (w *trackingResponseWriter) Flush() {
	w.wrote = true
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
