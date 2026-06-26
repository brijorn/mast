package api

import (
	"encoding/json"
	"net/http"

	"github.com/brijorn/mast/internal/program"
)

type runLogsResponse struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func (s *Server) RegisterProgram(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	var req program.RegisterOptions
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	registered, err := s.programs.Register(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(registered); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) ListPrograms(w http.ResponseWriter, _ *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.programs.ListPrograms()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) StartRuns(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	var req program.StartOptions
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	runs, err := s.programs.Start(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(runs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) ListRuns(w http.ResponseWriter, _ *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.programs.ListRuns()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) StopRun(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	run, err := s.programs.Stop(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) RunLogs(w http.ResponseWriter, r *http.Request) {
	if s.programs == nil {
		http.Error(w, "program runner not configured", http.StatusServiceUnavailable)
		return
	}

	stdout, stderr, err := s.programs.Logs(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(runLogsResponse{Stdout: stdout, Stderr: stderr}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
