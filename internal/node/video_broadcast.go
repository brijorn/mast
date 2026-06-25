package node

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

type VideoPacket struct {
	PTS      uint64
	Config   bool
	Keyframe bool
	Data     []byte
}

func (s *StreamSession) readVideoPacket() (*VideoPacket, error) {
	header := make([]byte, 12)
	_, err := io.ReadFull(s.videoConn, header)
	if err != nil {
		return nil, err
	}

	ptsAndFlags := binary.BigEndian.Uint64(header[:8])
	size := binary.BigEndian.Uint32(header[8:12])

	config := ptsAndFlags&(1<<63) != 0
	keyFrame := ptsAndFlags&(1<<62) != 0
	pts := ptsAndFlags & ((1 << 62) - 1)

	data := make([]byte, size)
	_, err = io.ReadFull(s.videoConn, data)
	if err != nil {
		return nil, err
	}

	nalTypes := h264NALTypes(data)
	config = config || containsNALType(nalTypes, 7) || containsNALType(nalTypes, 8)
	keyFrame = keyFrame && containsNALType(nalTypes, 5)
	keyFrame = keyFrame || containsNALType(nalTypes, 5)

	return &VideoPacket{
		PTS:      pts,
		Config:   config,
		Keyframe: keyFrame,
		Data:     data,
	}, nil
}

type videoSubscriber chan VideoPacket

type videoBroadcaster struct {
	mu             sync.Mutex
	subscribers    map[videoSubscriber]struct{}
	latestConfig   *VideoPacket
	latestKeyframe *VideoPacket
	done           chan struct{}
}

func newVideoBroadcaster() *videoBroadcaster {
	return &videoBroadcaster{
		subscribers: make(map[videoSubscriber]struct{}),
		done:        make(chan struct{}),
	}
}

func (b *videoBroadcaster) Subscribe() (<-chan VideoPacket, func()) {
	ch := make(chan VideoPacket, 32)

	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	replayPackets := b.replayPackets()
	b.mu.Unlock()

	for _, packet := range replayPackets {
		ch <- packet
	}

	unsubscribe := func() {
		b.mu.Lock()
		delete(b.subscribers, ch)
		close(ch)
		b.mu.Unlock()
	}

	return ch, unsubscribe
}

func (b *videoBroadcaster) broadcast(packet VideoPacket) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if packet.Config {
		b.latestConfig = cloneVideoPacket(packet)
	}
	if packet.Keyframe {
		b.latestKeyframe = cloneVideoPacket(packet)
	}

	for ch := range b.subscribers {
		select {
		case ch <- packet:
		default:
			// drop frame
		}
	}
}

func (b *videoBroadcaster) replayPackets() []VideoPacket {
	var packets []VideoPacket
	if b.latestConfig != nil {
		packets = append(packets, *cloneVideoPacket(*b.latestConfig))
	}
	if b.latestKeyframe != nil {
		packets = append(packets, *cloneVideoPacket(*b.latestKeyframe))
	}
	return packets
}

func cloneVideoPacket(packet VideoPacket) *VideoPacket {
	data := make([]byte, len(packet.Data))
	copy(data, packet.Data)
	packet.Data = data
	return &packet
}

func h264NALTypes(data []byte) []byte {
	var types []byte
	for i := 0; i+3 < len(data); i++ {
		startCodeLen := 0
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			startCodeLen = 3
		} else if i+4 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			startCodeLen = 4
		}
		if startCodeLen == 0 {
			continue
		}

		nalOffset := i + startCodeLen
		if nalOffset < len(data) {
			types = append(types, data[nalOffset]&0x1f)
		}
		i = nalOffset
	}
	return types
}

func containsNALType(types []byte, target byte) bool {
	for _, typ := range types {
		if typ == target {
			return true
		}
	}
	return false
}

func (s *StreamSession) broadcastVideo() {
	for {
		packet, err := s.readVideoPacket()
		if err != nil {
			return
		}
		s.videoBroadcaster.broadcast(*packet)
	}
}

func (s *StreamSession) SubscribeVideo() (<-chan VideoPacket, func(), error) {
	if s.videoBroadcaster == nil {
		return nil, nil, errors.New("video broadcaster not available")
	}

	ch, unsubscribe := s.videoBroadcaster.Subscribe()
	return ch, unsubscribe, nil
}
