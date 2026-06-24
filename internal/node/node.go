package node

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/brijorn/mast/internal/transport"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type PeerConn struct {
	conn           *websocket.Conn
	mu             sync.Mutex
	AndroidEnabled bool
	Addr           string
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
	adb            adbRunner
	streams        map[string]*streamEntry
	streamsMu      sync.RWMutex
}

func NewNode(id string, addr string, advertiseHost string, androidEnabled bool) (*Node, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
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
		adb:            realADB{},
	}, nil
}

func (n *Node) GetPeer(peerID string) (*PeerConn, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	peer, ok := n.Peers[peerID]
	return peer, ok
}

type peerRequest struct {
	transport.RawMessage
	Payload any `json:"payload"`
}

func (n *Node) sendPeerRequest(peerID string, messageType string, payload any) error {
	peer, ok := n.GetPeer(peerID)
	if !ok {
		return errors.New("peer not found")
	}

	msg := &peerRequest{
		RawMessage: transport.RawMessage{
			Type:      messageType,
			ID:        uuid.NewString(),
			From:      n.ID,
			To:        peerID,
			Timestamp: time.Now(),
		},
		Payload: payload,
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	if err := peer.WriteMessage(websocket.TextMessage, encoded); err != nil {
		return err
	}

	return nil
}

// Listen to incoming connections from other masts
func (n *Node) Listen() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := n.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Print("upgrade:", err)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		go n.handleConnection(&PeerConn{conn: conn, Addr: host}, "")
	})
	return http.Serve(n.Listener, mux)
}

// Connect to another mast
func (n *Node) Connect(addr string) error {
	conn, _, err := websocket.DefaultDialer.Dial(addr, nil)
	if err != nil {
		return err
	}

	u, err := url.Parse(addr)
	if err != nil {
		return err
	}

	go n.handleConnection(&PeerConn{conn: conn, Addr: u.Hostname()}, addr)
	return nil
}

func (n *Node) reconnect(addr string) {
	backoff := time.Second
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-time.After(backoff):
			log.Println("reconnecting to ", addr)
			if err := n.Connect(addr); err != nil {
				log.Println("reconnect failed:", err)
				backoff *= 2
				if backoff > 60*time.Second {
					log.Println("failed to reconnect to ", addr)
					return
				}
				continue
			}
			backoff = time.Second
			return
		}
	}
}

func (n *Node) Close() error {
	n.cancel()
	n.mu.Lock()
	defer n.mu.Unlock()
	var err error
	for _, peer := range n.Peers {
		if closeErr := peer.conn.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	if closeErr := n.Listener.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}

func (n *Node) handleConnection(peer *PeerConn, addr string) {
	defer func() { _ = peer.conn.Close() }()
	registered := false

	// send registration first if we initiated
	if addr != "" {
		msg := transport.ConnectionRequest{
			RawMessage: transport.RawMessage{
				Type:      transport.TypeConnectionRequest,
				ID:        uuid.NewString(),
				Timestamp: time.Now(),
				From:      n.ID,
			},
			Payload: transport.ConnectionRequestPayload{
				AndroidEnabled: n.AndroidEnabled,
			},
		}

		data, err := json.Marshal(msg)
		if err != nil {
			log.Print("marshal:", err)
			return
		}

		if err := peer.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Print("write:", err)
			return
		}
	}

	done := make(chan struct{})
	defer close(done)
	err := peer.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	if err != nil {
		return
	}
	for {
		_, message, err := peer.conn.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			n.mu.Lock()
			for id, p := range n.Peers {
				if p == peer {
					log.Println("removing peer", id)
					delete(n.Peers, id)
					break
				}
			}
			n.mu.Unlock()

			if addr != "" {
				go n.reconnect(addr)
			}
			break
		}
		if err := peer.conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			log.Println("set:", err)
			return
		}

		var raw transport.RawMessage
		if err := json.Unmarshal(message, &raw); err != nil {
			log.Println("decode:", err)
			continue
		}

		if !registered && raw.MessageType() != transport.TypeConnectionRequest {
			log.Println("first message must be registration")
			break
		}

		switch raw.MessageType() {
		case transport.TypeConnectionRequest:
			var req transport.ConnectionRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode connection request:", err)
				break
			}

			n.mu.Lock()
			if old, exists := n.Peers[raw.From]; exists {
				err := old.conn.Close()
				if err != nil {
					return
				}
			}
			peer.AndroidEnabled = req.Payload.AndroidEnabled
			n.Peers[raw.From] = peer
			n.mu.Unlock()

			if !registered {
				registered = true
				// receiver sends registration back
				if addr == "" {
					msg := transport.ConnectionRequest{
						RawMessage: transport.RawMessage{
							Type:      transport.TypeConnectionRequest,
							ID:        uuid.NewString(),
							From:      n.ID,
							Timestamp: time.Now(),
						},
						Payload: transport.ConnectionRequestPayload{
							AndroidEnabled: n.AndroidEnabled,
						},
					}

					data, err := json.Marshal(msg)
					if err != nil {
						log.Println("marshal:", err)
						break
					}
					if err := peer.WriteMessage(websocket.TextMessage, data); err != nil {
						log.Println("write connection request:", err)
						break
					}
				}

				peer.conn.SetPongHandler(func(string) error {
					log.Println(n.ID, "pong received from", raw.From)
					if err := peer.conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
						log.Println("set:", err)
					}
					return nil
				})

				go func() {
					ticker := time.NewTicker(n.PingInterval)
					defer ticker.Stop()
					for {
						select {
						case <-ticker.C:
							log.Println(n.ID, "sending ping to", raw.From)
							if err := peer.WriteMessage(websocket.PingMessage, nil); err != nil {
								log.Println("write ping:", err)
								return
							}
						case <-n.ctx.Done():
							return
						case <-done:
							return
						}
					}
				}()

			}

		case transport.TypeStartStreamRequest:
			var req transport.StartStreamRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode start stream request:", err)
				break
			}
			if _, err := n.EnsureStream(req.Payload.Serial, req.Payload.Options); err != nil {
				log.Println("start stream:", err)
			}
		case transport.TypeStopStreamRequest:
			var req transport.StopStreamRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode stop stream request:", err)
				break
			}

			if err := n.StopStream(req.Payload.Serial); err != nil {
				log.Println("stop stream:", err)
				break
			}
		case transport.TypeTapRequest:
			var req transport.TapRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode tap request:", err)
				break
			}

			if err := n.tapLocal(req.Payload.Serial, req.Payload.X, req.Payload.Y); err != nil {
				log.Println("tap:", err)
				break
			}
		case transport.TypeSwipeRequest:
			var req transport.SwipeRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode swipe request:", err)
				break
			}

			if err := n.swipeLocal(req.Payload.Serial, req.Payload.StartX, req.Payload.StartY, req.Payload.EndX, req.Payload.EndY); err != nil {
				log.Println("swipe:", err)
				break
			}
		}
	}
}
