package node

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/update"
	"github.com/danielpaulus/go-ios/ios/tunnel"
	"github.com/gorilla/websocket"
)

type PeerConn struct {
	conn           *websocket.Conn
	mu             sync.Mutex
	AndroidEnabled bool
	IOSEnabled     bool
	ProxyEnabled   bool
	Addr           string
	Target         string
	ADBPort        int
	Version        string
	Commit         string
	BuildDate      string
	DeviceError    string
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
	IOSEnabled     bool
	ProxyEnabled   bool
	ADBPort        int
	adb            adbRunner
	updateChecker  update.UpdateChecker
	updateApplier  update.UpdateApplier
	pendingMu      sync.Mutex
	pending        map[string]chan peerRPCResponse
	streams        map[string]*streamEntry
	streamsMu      sync.RWMutex
	batteryMu      sync.RWMutex
	batteryCache   map[string]batterySnapshot
	configMu       sync.RWMutex
	configPath     string
	configState    mastconfig.Config
	configReady    bool
	configApplier  RuntimeConfigApplier
	iosMu          sync.Mutex
	iosTunnelMgr   *tunnel.TunnelManager
}

func NewNode(id string, addr string, advertiseHost string, androidEnabled bool, iosEnabled bool, proxyEnabled bool) (*Node, error) {
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
		batteryCache:   make(map[string]batterySnapshot),
		PingInterval:   30 * time.Second,
		AndroidEnabled: androidEnabled,
		IOSEnabled:     iosEnabled,
		ProxyEnabled:   proxyEnabled,
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
