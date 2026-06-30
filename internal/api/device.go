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

func (s *Server) Screenshot(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	png, err := s.node.Screenshot(serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(png); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) DeviceDNS(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	status, err := s.node.DeviceDNS(serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) ToggleDeviceDNS(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	status, err := s.node.ToggleDeviceDNS(serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
