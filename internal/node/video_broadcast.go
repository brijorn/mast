package node

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

const videoReplayKeyframeMaxAge = time.Minute

type VideoPacket struct {
	PTS        uint64
	Config     bool
	Keyframe   bool
	Data       []byte
	sequence   uint64
	receivedAt time.Time
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
	nextSequence   uint64
	done           chan struct{}
}

func newVideoBroadcaster() *videoBroadcaster {
	return &videoBroadcaster{
		subscribers: make(map[videoSubscriber]struct{}),
		done:        make(chan struct{}),
	}
}

func (b *videoBroadcaster) Subscribe() (<-chan VideoPacket, func()) {
	ch := make(chan VideoPacket, 256)

	b.mu.Lock()
	for _, packet := range b.replayPackets(time.Now()) {
		ch <- packet
	}
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

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

	b.nextSequence++
	packet.sequence = b.nextSequence
	packet.receivedAt = time.Now()

	if packet.Config {
		b.latestConfig = cloneVideoPacketForConfigReplay(packet)
	}
	if packet.Keyframe {
		b.latestKeyframe = cloneVideoPacket(packet)
	}

	for ch := range b.subscribers {
		select {
		case ch <- packet:
		default:
			// Keep the stream reader non-blocking. A full subscriber queue means
			// that client is already behind; dropping here is preferable to
			// stalling the scrcpy reader for every other client.
		}
	}
}

func (b *videoBroadcaster) replayPackets(now time.Time) []VideoPacket {
	var packets []VideoPacket
	if b.latestConfig != nil && (!b.latestConfig.Keyframe || isFreshVideoPacket(*b.latestConfig, now)) {
		packets = append(packets, *cloneVideoPacket(*b.latestConfig))
	}
	if b.latestKeyframe != nil &&
		isFreshVideoPacket(*b.latestKeyframe, now) &&
		(b.latestConfig == nil || b.latestKeyframe.sequence != b.latestConfig.sequence || !b.latestConfig.Keyframe) {
		packets = append(packets, *cloneVideoPacket(*b.latestKeyframe))
	}
	return packets
}

func isFreshVideoPacket(packet VideoPacket, now time.Time) bool {
	return packet.receivedAt.IsZero() || now.Sub(packet.receivedAt) <= videoReplayKeyframeMaxAge
}

func cloneVideoPacket(packet VideoPacket) *VideoPacket {
	data := make([]byte, len(packet.Data))
	copy(data, packet.Data)
	packet.Data = data
	return &packet
}

func cloneVideoPacketForConfigReplay(packet VideoPacket) *VideoPacket {
	cloned := cloneVideoPacket(packet)
	if configData := h264ConfigData(cloned.Data); len(configData) > 0 {
		cloned.Data = configData
		cloned.Config = true
		cloned.Keyframe = false
	}
	return cloned
}

func h264NALTypes(data []byte) []byte {
	var types []byte
	for _, nal := range h264NALRanges(data) {
		types = append(types, nal.typ)
	}
	return types
}

func h264ConfigData(data []byte) []byte {
	var config []byte
	for _, nal := range h264NALRanges(data) {
		if nal.typ != 7 && nal.typ != 8 {
			continue
		}
		config = append(config, data[nal.start:nal.end]...)
	}
	return config
}

type h264NALRange struct {
	start int
	end   int
	typ   byte
}

func h264NALRanges(data []byte) []h264NALRange {
	var ranges []h264NALRange
	for i := 0; i+3 < len(data); i++ {
		startCodeLen := h264StartCodeLen(data, i)
		if startCodeLen == 0 {
			continue
		}

		nalOffset := i + startCodeLen
		if nalOffset >= len(data) {
			break
		}

		ranges = append(ranges, h264NALRange{
			start: i,
			typ:   data[nalOffset] & 0x1f,
		})
		i = nalOffset
	}

	for i := range ranges {
		if i+1 < len(ranges) {
			ranges[i].end = ranges[i+1].start
		} else {
			ranges[i].end = len(data)
		}
	}
	return ranges
}

func h264StartCodeLen(data []byte, offset int) int {
	if offset+3 <= len(data) &&
		data[offset] == 0 &&
		data[offset+1] == 0 &&
		data[offset+2] == 1 {
		return 3
	}
	if offset+4 <= len(data) &&
		data[offset] == 0 &&
		data[offset+1] == 0 &&
		data[offset+2] == 0 &&
		data[offset+3] == 1 {
		return 4
	}
	return 0
}

func containsNALType(types []byte, target byte) bool {
	for _, typ := range types {
		if typ == target {
			return true
		}
	}
	return false
}

func (s *StreamSession) broadcastVideo(cleanup func()) {
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()
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
