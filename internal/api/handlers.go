package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
