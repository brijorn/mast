package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/brijorn/mast/internal/node"
	"github.com/gorilla/websocket"
)

var videoUpgrader = websocket.Upgrader{
	// The API's guard has already refused an origin the node does not allow,
	// so the upgrade accepts whatever reaches it.
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) StreamVideo(w http.ResponseWriter, r *http.Request) {
	serial := r.URL.Query().Get("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}
	viewer := r.URL.Query().Get("viewer")
	if viewer == "" {
		http.Error(w, "viewer required", http.StatusBadRequest)
		return
	}

	conn, err := videoUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	key := videoViewerKey{serial: serial, viewer: viewer}
	s.replaceVideoViewer(key, conn)
	defer s.releaseVideoViewer(key, conn)

	ctx, cancel := context.WithCancel(r.Context())
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancel()
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	// A viewer that does not say what rate it needs is served as if it needs
	// them all, so an older client is never downgraded under its user.
	maxFPS, _ := strconv.Atoi(r.URL.Query().Get("max_fps"))
	streamErr := s.node.StreamVideo(ctx, serial, conn, max(maxFPS, 0))
	if node.IsStreamNotFound(streamErr) {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(node.VideoCloseStreamNotFound, "stream not found"),
			time.Now().Add(time.Second),
		)
	}
	cancel()
	_ = conn.Close()
	<-readerDone
}

func (s *Server) replaceVideoViewer(key videoViewerKey, conn *websocket.Conn) {
	s.videoMu.Lock()
	previous := s.videoConns[key]
	s.videoConns[key] = conn
	s.videoMu.Unlock()
	if previous != nil && previous != conn {
		_ = previous.Close()
	}
}

func (s *Server) releaseVideoViewer(key videoViewerKey, conn *websocket.Conn) {
	s.videoMu.Lock()
	if s.videoConns[key] == conn {
		delete(s.videoConns, key)
	}
	s.videoMu.Unlock()
}
