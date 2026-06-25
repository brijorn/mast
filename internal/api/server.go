package api

import (
	"context"
	"net/http"

	"github.com/brijorn/mast/internal/node"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/brijorn/mast/internal/update"
)

type nodeBackend interface {
	ListNodes() []node.NodeInfo
	ListDevices() ([]node.DeviceInfo, error)
	Connect(addr string) error
	CheckNodeUpdate(ctx context.Context, nodeID string) (*update.CheckResult, error)
	ApplyNodeUpdate(ctx context.Context, nodeID string, opts update.ApplyOptions) (*update.ApplyResult, error)
	GetStream(serial string) (*node.StreamSession, error)
	EnsureStream(serial string, opts streamcfg.Options) (*node.StreamSession, error)
	Touch(serial string, action string, x, y int) error
	Tap(serial string, x, y int) error
	Swipe(serial string, startX, startY, endX, endY int) error
}

type Server struct {
	node          nodeBackend
	updateChecker update.UpdateChecker
}

func NewServer(n nodeBackend) *Server {
	return &Server{
		node:          n,
		updateChecker: &update.Checker{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/devices", s.ListDevices)
	mux.HandleFunc("GET /api/nodes", s.ListNodes)
	mux.HandleFunc("POST /api/peers", s.AddPeer)
	mux.HandleFunc("GET /api/nodes/{id}/update", s.CheckNodeUpdate)
	mux.HandleFunc("POST /api/nodes/{id}/update", s.ApplyNodeUpdate)

	mux.HandleFunc("GET /api/update", s.CheckUpdate)
	mux.HandleFunc("POST /api/update", s.ApplyUpdate)

	mux.HandleFunc("POST /api/streams", s.StartStream)
	mux.HandleFunc("GET /api/streams/video", s.StreamVideo)

	mux.HandleFunc("POST /api/control/touch", s.Touch)
	mux.HandleFunc("POST /api/control/tap", s.Tap)
	mux.HandleFunc("POST /api/control/swipe", s.Swipe)
	return mux
}

func (s *Server) Listen(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}
