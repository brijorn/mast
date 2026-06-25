package api

import (
	"encoding/binary"
	"net/http"

	"github.com/brijorn/mast/internal/node"
	"github.com/gorilla/websocket"
)

var videoUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) StreamVideo(w http.ResponseWriter, r *http.Request) {
	serial := r.URL.Query().Get("serial")
	if serial == "" {
		http.Error(w, "serial required", http.StatusBadRequest)
		return
	}

	stream, err := s.node.GetStream(serial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	packets, unsubscribe, err := stream.SubscribeVideo()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer unsubscribe()

	conn, err := videoUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	for packet := range packets {
		data := encodeVideoPacket(packet)
		if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			return
		}
	}
}

func encodeVideoPacket(packet node.VideoPacket) []byte {
	flags := byte(0)
	if packet.Config {
		flags |= 1
	}
	if packet.Keyframe {
		flags |= 2
	}

	buf := make([]byte, 13+len(packet.Data))
	buf[0] = flags
	binary.BigEndian.PutUint64(buf[1:9], packet.PTS)
	binary.BigEndian.PutUint32(buf[9:13], uint32(len(packet.Data)))
	copy(buf[13:], packet.Data)
	return buf
}
