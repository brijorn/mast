package node

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/brijorn/mast/internal/update"
	"github.com/gorilla/websocket"
)

type PeerConn struct {
	conn           *websocket.Conn
	mu             sync.Mutex
	AndroidEnabled bool
	Addr           string
	ADBHost        string
	ADBPort        int
	Version        string
	Commit         string
	BuildDate      string
}

func (p *PeerConn) WriteMessage(messageType int, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn.WriteMessage(messageType, data)
}

type Node struct {
	ID             string
	AdvertiseHost  string
	Listener       net.Listener
	mu             sync.RWMutex
	Peers          map[string]*PeerConn
	Client         http.Client
	Upgrader       websocket.Upgrader
	ctx            context.Context
	cancel         context.CancelFunc
	PingInterval   time.Duration
	AndroidEnabled bool
	ADBHost        string
	ADBPort        int
	adb            adbRunner
	updateChecker  update.UpdateChecker
	updateApplier  update.UpdateApplier
	pendingMu      sync.Mutex
	pending        map[string]chan peerRPCResponse
	streams        map[string]*streamEntry
	streamsMu      sync.RWMutex
}

func NewNode(id string, addr string, advertiseHost string, androidEnabled bool) (*Node, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	updateChecker := &update.Checker{}
	return &Node{
		ID:             id,
		Listener:       ln,
		Peers:          make(map[string]*PeerConn),
		ctx:            ctx,
		cancel:         cancel,
		AdvertiseHost:  advertiseHost,
		streams:        make(map[string]*streamEntry),
		PingInterval:   30 * time.Second,
		AndroidEnabled: androidEnabled,
		ADBHost:        "127.0.0.1",
		ADBPort:        5037,
		adb:            realADB{},
		updateChecker:  updateChecker,
		updateApplier:  &update.Applier{Checker: updateChecker},
		pending:        make(map[string]chan peerRPCResponse),
	}, nil
}

func (n *Node) GetPeer(peerID string) (*PeerConn, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	peer, ok := n.Peers[peerID]
	return peer, ok
}
