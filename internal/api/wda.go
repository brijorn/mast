package api

import (
	"encoding/json"
	"net/http"

	"github.com/brijorn/mast/internal/node"
)

type nodeElementRect = node.ElementBounds

func (s *Server) wda() wdaBackend {
	return s.node.(wdaBackend)
}

// These endpoints proxy WebDriverAgent's element and source operations to the
// owning node's iOS device, for a program navigating Settings or an ad's
// dialog through WDA's element tree rather than through taps.

type wdaBackend interface {
	DeviceSource(serial string) (string, error)
	FindElement(serial, using, value string) (string, error)
	FindElements(serial, using, value string) ([]string, error)
	ClickElement(serial, id string) error
	ClearElement(serial, id string) error
	SetElementValue(serial, id, value string) error
	ElementRect(serial, id string) (nodeElementRect, error)
	ElementAttribute(serial, id, name string) (string, error)
}

type findRequest struct {
	Serial string `json:"serial"`
	Using  string `json:"using"`
	Value  string `json:"value"`
}

type elementRequest struct {
	Serial string `json:"serial"`
	ID     string `json:"id"`
	Value  string `json:"value,omitempty"`
	Name   string `json:"name,omitempty"`
}

func (s *Server) DeviceSource(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}
	source, err := s.wda().DeviceSource(serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"source": source})
}

func (s *Server) FindElements(w http.ResponseWriter, r *http.Request) {
	var req findRequest
	if !decodeJSON(w, r, &req) || !requireSerial(w, req.Serial) {
		return
	}
	ids, err := s.wda().FindElements(req.Serial, req.Using, req.Value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ids": ids})
}

func (s *Server) FindElement(w http.ResponseWriter, r *http.Request) {
	var req findRequest
	if !decodeJSON(w, r, &req) || !requireSerial(w, req.Serial) {
		return
	}
	id, err := s.wda().FindElement(req.Serial, req.Using, req.Value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id})
}

func (s *Server) ClickElement(w http.ResponseWriter, r *http.Request) {
	var req elementRequest
	if !decodeJSON(w, r, &req) || !requireSerial(w, req.Serial) {
		return
	}
	if err := s.wda().ClickElement(req.Serial, req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ClearElement(w http.ResponseWriter, r *http.Request) {
	var req elementRequest
	if !decodeJSON(w, r, &req) || !requireSerial(w, req.Serial) {
		return
	}
	if err := s.wda().ClearElement(req.Serial, req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) SetElementValue(w http.ResponseWriter, r *http.Request) {
	var req elementRequest
	if !decodeJSON(w, r, &req) || !requireSerial(w, req.Serial) {
		return
	}
	if err := s.wda().SetElementValue(req.Serial, req.ID, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ElementRect(w http.ResponseWriter, r *http.Request) {
	var req elementRequest
	if !decodeJSON(w, r, &req) || !requireSerial(w, req.Serial) {
		return
	}
	rect, err := s.wda().ElementRect(req.Serial, req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rect)
}

func (s *Server) ElementAttribute(w http.ResponseWriter, r *http.Request) {
	var req elementRequest
	if !decodeJSON(w, r, &req) || !requireSerial(w, req.Serial) {
		return
	}
	value, err := s.wda().ElementAttribute(req.Serial, req.ID, req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"value": value})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func requireSerial(w http.ResponseWriter, serial string) bool {
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
