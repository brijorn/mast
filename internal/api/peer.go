package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/brijorn/mast/internal/peer"
)

type addPeerRequest struct {
	Target string `json:"target"`
	URL    string `json:"url"`
}

type addPeerResponse struct {
	URL string `json:"url"`
}

func (s *Server) AddPeer(w http.ResponseWriter, r *http.Request) {
	var req addPeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(r.Body)

	target := req.Target
	if target == "" {
		target = req.URL
	}

	peerURL, err := peer.NormalizeTarget(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.node.Connect(peerURL); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(addPeerResponse{URL: peerURL}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) RemovePeer(w http.ResponseWriter, r *http.Request) {
	var req addPeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(r.Body)

	target := req.Target
	if target == "" {
		target = req.URL
	}

	peerURL, err := peer.NormalizeTarget(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.node.DisconnectPeer(peerURL)
	w.WriteHeader(http.StatusNoContent)
}
