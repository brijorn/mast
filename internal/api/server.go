package api

import (
	"net/http"

	"github.com/brijorn/mast/internal/node"
	streamcfg "github.com/brijorn/mast/internal/stream"
)

type nodeBackend interface {
	ListDevices() ([]node.DeviceInfo, error)
	GetStream(serial string) (*node.StreamSession, error)
	EnsureStream(serial string, opts streamcfg.Options) (*node.StreamSession, error)
	Touch(serial string, action string, x, y int) error
	Tap(serial string, x, y int) error
	Swipe(serial string, startX, startY, endX, endY int) error
}

type Server struct {
	node nodeBackend
}

func NewServer(n nodeBackend) *Server {
	return &Server{
		node: n,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/devices", s.ListDevices)
	mux.HandleFunc("/api/streams", s.StartStream)
	mux.HandleFunc("/api/streams/video", s.StreamVideo)
	mux.HandleFunc("/api/control/touch", s.Touch)
	mux.HandleFunc("/api/control/tap", s.Tap)
	mux.HandleFunc("/api/control/swipe", s.Swipe)
	return mux
}

func (s *Server) Listen(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}
