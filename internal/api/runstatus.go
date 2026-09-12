package api

import (
	"encoding/json"
	"net/http"
)

// GetRun answers from the process host without waiting for unrelated peers.
// Remote lookup uses the existing run routing boundary and request context.
func (s *Server) GetRun(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}
	for _, run := range s.programs.ListRuns() {
		if run.ID != r.PathValue("id") {
			continue
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(run)
		return
	}
	if s.proxyToPeerWithRun(w, r, "/api/runs/"+r.PathValue("id")) {
		return
	}
	http.Error(w, "run not found", http.StatusNotFound)
}
