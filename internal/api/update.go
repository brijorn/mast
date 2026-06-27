package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/brijorn/mast/internal/update"
)

func (s *Server) CheckUpdate(w http.ResponseWriter, r *http.Request) {
	res, err := s.updateChecker.Check(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

}

func (s *Server) ApplyUpdate(w http.ResponseWriter, r *http.Request) {
	var opts update.ApplyOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(r.Body)

	applier := update.Applier{
		Checker: s.updateChecker,
	}

	res, err := applier.Apply(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var restarter restartBackend
	if opts.Restart && res.Updated {
		var ok bool
		restarter, ok = s.node.(restartBackend)
		if !ok {
			http.Error(w, "restart not supported", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if restarter != nil {
		if err := http.NewResponseController(w).Flush(); err != nil {
			log.Println("flush update response:", err)
		}
		if err := restarter.ScheduleRestart(0); err != nil {
			log.Println("schedule restart:", err)
		}
	}
}

func (s *Server) CheckNodeUpdate(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	res, err := s.node.CheckNodeUpdate(r.Context(), nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) ApplyNodeUpdate(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	var opts update.ApplyOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(r.Body)

	res, err := s.node.ApplyNodeUpdate(r.Context(), nodeID, opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
