package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type configUpdateRequest struct {
	Values map[string]any `json:"values"`
}

func (s *Server) GetNodeConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	cfg, err := s.node.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) UpdateNodeConfig(w http.ResponseWriter, r *http.Request) {
	values, err := decodeConfigValues(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(r.Body)

	result, err := s.node.UpdateNodeConfig(r.Context(), r.PathValue("id"), values)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "invalid config key") || strings.Contains(err.Error(), "invalid runner key") || strings.Contains(err.Error(), "invalid syntax") || strings.Contains(err.Error(), "battery_protection.") {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func decodeConfigValues(body io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("request body required")
	}

	var wrapped configUpdateRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&wrapped); err != nil {
		return nil, err
	}
	if wrapped.Values != nil {
		return stringifyConfigValues(wrapped.Values)
	}

	var direct map[string]any
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&direct); err != nil {
		return nil, err
	}
	return stringifyConfigValues(direct)
}

func stringifyConfigValues(values map[string]any) (map[string]string, error) {
	out := make(map[string]string)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		if key == "runners" || key == "battery_protection" {
			nested, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s must be an object", key)
			}
			nestedKeys := make([]string, 0, len(nested))
			for nestedKey := range nested {
				nestedKeys = append(nestedKeys, nestedKey)
			}
			sort.Strings(nestedKeys)
			for _, nestedKey := range nestedKeys {
				str, err := stringifyConfigValue(nested[nestedKey])
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", key, nestedKey, err)
				}
				out[key+"."+nestedKey] = str
			}
			continue
		}

		str, err := stringifyConfigValue(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = str
	}
	return out, nil
}

func stringifyConfigValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case json.Number:
		return v.String(), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("value must be a string, number, boolean, or null")
	}
}
