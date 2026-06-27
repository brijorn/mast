package api

import (
	"context"
	"net/http"
	"time"

	"github.com/brijorn/mast/internal/node"
	"github.com/brijorn/mast/internal/program"
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
	PressKey(serial string, keycode uint32) error
}

type restartBackend interface {
	ScheduleRestart(delay time.Duration) error
}

type Server struct {
	node          nodeBackend
	programs      programBackend
	updateChecker update.UpdateChecker
}

type programBackend interface {
	Register(opts program.RegisterOptions) (*program.Program, error)
	RegisterUpload(opts program.RegisterUploadOptions) (*program.Program, error)
	ListPrograms() []program.Program
	Start(opts program.StartOptions) ([]program.Run, error)
	ListRuns() []program.Run
	Stop(id string) (*program.Run, error)
	Logs(id string) (string, string, error)
	CleanupRun(id string) (*program.Run, error)
}

func NewServer(n nodeBackend, programs ...programBackend) *Server {
	var programStore programBackend
	if len(programs) > 0 {
		programStore = programs[0]
	}
	return &Server{
		node:          n,
		programs:      programStore,
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

	mux.HandleFunc("GET /api/programs", s.ListPrograms)
	mux.HandleFunc("POST /api/programs", s.RegisterProgram)
	mux.HandleFunc("POST /api/programs/upload", s.UploadProgram)
	mux.HandleFunc("GET /api/runs", s.ListRuns)
	mux.HandleFunc("POST /api/runs", s.StartRuns)
	mux.HandleFunc("POST /api/runs/{id}/stop", s.StopRun)
	mux.HandleFunc("GET /api/runs/{id}/logs", s.RunLogs)
	mux.HandleFunc("POST /api/runs/{id}/cleanup", s.CleanupRun)

	mux.HandleFunc("POST /api/control/touch", s.Touch)
	mux.HandleFunc("POST /api/control/tap", s.Tap)
	mux.HandleFunc("POST /api/control/swipe", s.Swipe)
	mux.HandleFunc("POST /api/control/keypress", s.PressKey)
	return mux
}

func (s *Server) Listen(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}
