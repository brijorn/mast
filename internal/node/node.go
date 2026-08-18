package node

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
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
	APIAddr        string
	Version        string
	Commit         string
	BuildDate      string
	DeviceError    string
	replaced       atomic.Bool
}

func (p *PeerConn) WriteMessage(messageType int, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn.WriteMessage(messageType, data)
}

type Node struct {
	ID                   string
	AdvertiseHost        string
	Listener             net.Listener
	mu                   sync.RWMutex
	Peers                map[string]*PeerConn
	Client               http.Client
	Upgrader             websocket.Upgrader
	ctx                  context.Context
	cancel               context.CancelFunc
	PingInterval         time.Duration
	AndroidEnabled       bool
	IOSEnabled           bool
	ProxyEnabled         bool
	ADBPort              int
	APIAddr              string
	adb                  adbRunner
	updateChecker        update.UpdateChecker
	updateApplier        update.UpdateApplier
	scheduleRestart      func(time.Duration) error
	pendingMu            sync.Mutex
	pending              map[string]chan peerRPCResponse
	streams              map[string]*streamEntry
	streamsMu            sync.RWMutex
	devToolsMu           sync.Mutex
	devToolsRelays       map[string]*devToolsRelay
	devToolsLoopback     map[string]int
	devicePowerMu        sync.Mutex
	devicePowerReady     map[string]bool
	devicePowerSessions  map[string]*devicePowerSession
	devicePowerStarting  map[string]*devicePowerAttempt
	devicePowerRetries   map[string]*devicePowerRetry
	devicePowerFailures  map[string]uint
	devicePowerOverride  map[string]bool
	deviceRotateOverride map[string]DeviceOrientation
	devicePowerAsserted  map[string]bool
	devicePowerWake      chan struct{}
	batteryMu            sync.RWMutex
	batteryCache         map[string]batterySnapshot
	identityMu           sync.RWMutex
	identityPath         string
	identityLoaded       bool
	identityCache        map[string]deviceIdentityEntry
	identitySaveMu       sync.Mutex
	addressMu            sync.RWMutex
	addressBySerial      map[string]string
	configMu             sync.RWMutex
	configPath           string
	configState          mastconfig.Config
	configReady          bool
	configApplier        RuntimeConfigApplier
	deviceBlacklist      map[string]struct{}
	iosMu                sync.Mutex
	iosTunnelMgr         *tunnel.TunnelManager
}

func NewNode(id string, addr string, advertiseHost string, androidEnabled bool, iosEnabled bool, proxyEnabled bool) (*Node, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	updateChecker := &update.Checker{}
	n := &Node{
		ID:                   id,
		Listener:             ln,
		Peers:                make(map[string]*PeerConn),
		ctx:                  ctx,
		cancel:               cancel,
		AdvertiseHost:        advertiseHost,
		streams:              make(map[string]*streamEntry),
		batteryCache:         make(map[string]batterySnapshot),
		identityCache:        make(map[string]deviceIdentityEntry),
		addressBySerial:      make(map[string]string),
		PingInterval:         30 * time.Second,
		AndroidEnabled:       androidEnabled,
		IOSEnabled:           iosEnabled,
		ProxyEnabled:         proxyEnabled,
		ADBPort:              5037,
		APIAddr:              mastconfig.DefaultAPIAddr,
		adb:                  realADB{},
		updateChecker:        updateChecker,
		updateApplier:        &update.Applier{Checker: updateChecker},
		scheduleRestart:      scheduleProcessRestartForPlatform,
		pending:              make(map[string]chan peerRPCResponse),
		deviceBlacklist:      make(map[string]struct{}),
		devicePowerReady:     make(map[string]bool),
		devicePowerSessions:  make(map[string]*devicePowerSession),
		devicePowerStarting:  make(map[string]*devicePowerAttempt),
		devicePowerRetries:   make(map[string]*devicePowerRetry),
		devicePowerFailures:  make(map[string]uint),
		devicePowerOverride:  make(map[string]bool),
		deviceRotateOverride: make(map[string]DeviceOrientation),
		devicePowerAsserted:  make(map[string]bool),
		devicePowerWake:      make(chan struct{}, 1),
	}
	go n.monitorDevicePowerPolicy()
	go n.monitorIdleStreams()
	return n, nil
}

func (n *Node) GetPeer(peerID string) (*PeerConn, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	peer, ok := n.Peers[peerID]
	return peer, ok
}
