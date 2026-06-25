package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) ListDevices(w http.ResponseWriter, _ *http.Request) {
	devices, err := s.node.ListDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(devices); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
