package api

import (
	"net/http"

	"github.com/brijorn/mast/internal/node"
	streamcfg "github.com/brijorn/mast/internal/stream"
)

type nodeBackend interface {
	ListDevices() ([]node.DeviceInfo, error)
	EnsureStream(serial string, opts streamcfg.Options) (*node.StreamSession, error)
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
	return mux
}

func (s *Server) Listen(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}
