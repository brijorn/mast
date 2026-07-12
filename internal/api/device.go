package api

import (
	"encoding/json"
	"net/http"

	"github.com/brijorn/mast/internal/node"
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

type geometryBackend interface {
	Geometry(serial string) (*node.DeviceGeometry, error)
}

func (s *Server) DeviceGeometry(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}
	backend, ok := s.node.(geometryBackend)
	if !ok {
		http.Error(w, "device geometry unavailable", http.StatusNotImplemented)
		return
	}
	geometry, err := backend.Geometry(serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(geometry); err != nil {
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

func (s *Server) SetDeviceDNS(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	var desired node.DeviceDNSStatus
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&desired); err != nil {
		http.Error(w, "invalid DNS configuration: "+err.Error(), http.StatusBadRequest)
		return
	}

	status, err := s.node.SetDeviceDNS(serial, desired)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
