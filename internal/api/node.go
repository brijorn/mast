package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) ListNodes(w http.ResponseWriter, _ *http.Request) {
	nodes := s.node.ListNodes()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(nodes); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
