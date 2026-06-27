package node

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/brijorn/mast/internal/transport"
	"github.com/brijorn/mast/internal/version"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Listen accepts incoming connections from other Mast nodes.
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

// Connect opens an outgoing peer websocket connection.
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
				ADBPort:        n.ADBPort,
				Version:        version.Version,
				Commit:         version.Commit,
				BuildDate:      version.Date,
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

		if n.deliverPeerRPCResponse(raw, message) {
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
			peer.ADBPort = req.Payload.ADBPort
			peer.Version = req.Payload.Version
			peer.Commit = req.Payload.Commit
			peer.BuildDate = req.Payload.BuildDate
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
							ADBPort:        n.ADBPort,
							Version:        version.Version,
							Commit:         version.Commit,
							BuildDate:      version.Date,
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

		case transport.TypeListDevicesRequest:
			var req transport.ListDevicesRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode list devices request:", err)
				break
			}
			n.handleListDevicesRequest(peer, req)
		case transport.TypeStartStreamRequest:
			var req transport.StartStreamRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode start stream request:", err)
				break
			}
			n.handleStartStreamRequest(peer, req)
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
		case transport.TypeTouchRequest:
			var req transport.TouchRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode touch request:", err)
				break
			}

			if err := n.touchLocal(req.Payload.Serial, req.Payload.Action, req.Payload.X, req.Payload.Y); err != nil {
				log.Println("touch:", err)
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
		case transport.TypePressKeyRequest:
			var req transport.PressKeyRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode press key request:", err)
				break
			}

			if err := n.pressKeyLocal(req.Payload.Serial, req.Payload.Keycode, req.Payload.MetaState); err != nil {
				log.Println("press key:", err)
				break
			}
		case transport.TypeUpdateCheckRequest:
			var req transport.UpdateCheckRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode update check request:", err)
				break
			}
			n.handleUpdateCheckRequest(peer, req)
		case transport.TypeUpdateApplyRequest:
			var req transport.UpdateApplyRequest
			if err := json.Unmarshal(message, &req); err != nil {
				log.Println("decode update apply request:", err)
				break
			}
			n.handleUpdateApplyRequest(peer, req)
		}
	}
}
