package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	mastconfig "github.com/brijorn/mast/internal/config"
)

type deviceBlacklistResponse struct {
	Serials             []string `json:"serials"`
	ChangedKeys         []string `json:"changed_keys,omitempty"`
	RestartRequired     bool     `json:"restart_required,omitempty"`
	RestartRequiredKeys []string `json:"restart_required_keys,omitempty"`
}

type deviceBlacklistRequest struct {
	Serial  string   `json:"serial"`
	Serials []string `json:"serials"`
}

func (s *Server) GetDeviceBlacklist(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.node.GetNodeConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeDeviceBlacklist(w, deviceBlacklistResponse{
		Serials: mastconfig.NormalizeDeviceBlacklist(cfg.DeviceBlacklist),
	})
}

func (s *Server) SetDeviceBlacklist(w http.ResponseWriter, r *http.Request) {
	s.deviceBlacklistMu.Lock()
	defer s.deviceBlacklistMu.Unlock()
	serials, err := decodeDeviceBlacklistSerials(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.updateDeviceBlacklist(w, r, serials)
}

func (s *Server) AddDeviceBlacklist(w http.ResponseWriter, r *http.Request) {
	s.deviceBlacklistMu.Lock()
	defer s.deviceBlacklistMu.Unlock()
	serial, err := decodeDeviceBlacklistSerial(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := s.node.GetNodeConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.updateDeviceBlacklist(w, r, mastconfig.AddDeviceBlacklist(cfg.DeviceBlacklist, serial))
}

func (s *Server) RemoveDeviceBlacklist(w http.ResponseWriter, r *http.Request) {
	s.deviceBlacklistMu.Lock()
	defer s.deviceBlacklistMu.Unlock()
	serial, err := decodeDeviceBlacklistSerial(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := s.node.GetNodeConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.updateDeviceBlacklist(w, r, mastconfig.RemoveDeviceBlacklist(cfg.DeviceBlacklist, serial))
}

func (s *Server) updateDeviceBlacklist(w http.ResponseWriter, r *http.Request, serials []string) {
	result, err := s.node.UpdateNodeConfig(r.Context(), r.PathValue("id"), map[string]string{
		"device_blacklist": mastconfig.FormatDeviceBlacklist(serials),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeDeviceBlacklist(w, deviceBlacklistResponse{
		Serials:             mastconfig.NormalizeDeviceBlacklist(result.Config.DeviceBlacklist),
		ChangedKeys:         result.ChangedKeys,
		RestartRequired:     result.RestartRequired,
		RestartRequiredKeys: result.RestartRequiredKeys,
	})
}

func decodeDeviceBlacklistSerials(body io.ReadCloser) ([]string, error) {
	defer func() {
		_ = body.Close()
	}()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("request body required")
	}

	var wrapped deviceBlacklistRequest
	if err := json.Unmarshal(data, &wrapped); err == nil && (wrapped.Serials != nil || wrapped.Serial != "") {
		if wrapped.Serial != "" {
			wrapped.Serials = append(wrapped.Serials, wrapped.Serial)
		}
		return mastconfig.NormalizeDeviceBlacklist(wrapped.Serials), nil
	}

	var serials []string
	if err := json.Unmarshal(data, &serials); err != nil {
		return nil, err
	}
	return mastconfig.NormalizeDeviceBlacklist(serials), nil
}

func decodeDeviceBlacklistSerial(body io.ReadCloser) (string, error) {
	serials, err := decodeDeviceBlacklistSerials(body)
	if err != nil {
		return "", err
	}
	if len(serials) != 1 {
		return "", fmt.Errorf("exactly one serial required")
	}
	return serials[0], nil
}

func writeDeviceBlacklist(w http.ResponseWriter, res deviceBlacklistResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
