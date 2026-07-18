package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	videoSubscriberMaxLatency     = 500 * time.Millisecond
	videoSubscriberMaxQueuedBytes = 1 << 20
	videoReplayMaxQueuedBytes     = 1 << 20
)

var errVideoPacketWaitTimeout = errors.New("video packet wait timeout")

type VideoPacket struct {
	PTS        uint64
	Config     bool
	Keyframe   bool
	Data       []byte
	encoded    []byte
	receivedAt time.Time
}

func encodeVideoPacket(packet *VideoPacket) []byte {
	if packet.encoded != nil {
		return packet.encoded
	}
	packet.encoded = make([]byte, 13+len(packet.Data))
	copy(packet.encoded[13:], packet.Data)
	writeVideoPacketHeader(packet.encoded, *packet)
	return packet.encoded
}

func writeVideoPacketHeader(buf []byte, packet VideoPacket) {
	flags := byte(0)
	if packet.Config {
		flags |= 1
	}
	if packet.Keyframe {
		flags |= 2
	}

	buf[0] = flags
	binary.BigEndian.PutUint64(buf[1:9], packet.PTS)
	binary.BigEndian.PutUint32(buf[9:13], uint32(len(packet.Data)))
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

	encoded := make([]byte, 13+size)
	data := encoded[13:]
	_, err = io.ReadFull(s.videoConn, data)
	if err != nil {
		return nil, err
	}

	nalTypes := h264NALTypes(data)
	config = config || containsNALType(nalTypes, 7) || containsNALType(nalTypes, 8)
	keyFrame = keyFrame || containsNALType(nalTypes, 5)

	packet := &VideoPacket{
		PTS:      pts,
		Config:   config,
		Keyframe: keyFrame,
		Data:     data,
		encoded:  encoded,
	}
	writeVideoPacketHeader(encoded, *packet)
	return packet, nil
}

type videoSubscription struct {
	mu                 sync.Mutex
	ready              chan struct{}
	queue              []VideoPacket
	head               int
	queuedBytes        int
	waitingForKeyframe bool
	closed             bool
}

func newVideoSubscription(initial []VideoPacket, waitingForKeyframe bool) *videoSubscription {
	s := &videoSubscription{
		ready:              make(chan struct{}, 1),
		waitingForKeyframe: waitingForKeyframe,
	}
	s.replaceLocked(initial, waitingForKeyframe)
	return s
}

func (s *videoSubscription) Next() (VideoPacket, bool) {
	packet, ok, _ := s.NextContext(context.Background(), 0)
	return packet, ok
}

func (s *videoSubscription) NextContext(ctx context.Context, maxWait time.Duration) (VideoPacket, bool, error) {
	var timeout <-chan time.Time
	var timer *time.Timer
	if maxWait > 0 {
		timer = time.NewTimer(maxWait)
		timeout = timer.C
		defer timer.Stop()
	}

	for {
		s.mu.Lock()
		if s.head < len(s.queue) {
			packet := s.queue[s.head]
			s.queue[s.head] = VideoPacket{}
			s.head++
			s.queuedBytes -= len(packet.Data)
			if s.head == len(s.queue) {
				s.queue = s.queue[:0]
				s.head = 0
			}
			s.mu.Unlock()
			return packet, true, nil
		}
		if s.closed {
			s.mu.Unlock()
			return VideoPacket{}, false, nil
		}
		s.mu.Unlock()

		select {
		case <-s.ready:
		case <-ctx.Done():
			return VideoPacket{}, false, ctx.Err()
		case <-timeout:
			return VideoPacket{}, false, errVideoPacketWaitTimeout
		}
	}
}

func (s *videoSubscription) enqueueDelta(packet VideoPacket) {
	s.enqueueDeltaAt(packet, time.Now())
}

func (s *videoSubscription) enqueueDeltaAt(packet VideoPacket, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.waitingForKeyframe {
		return
	}
	if packet.receivedAt.IsZero() {
		packet.receivedAt = now
	}

	s.appendLocked(packet)
	if s.queuedBytes > videoSubscriberMaxQueuedBytes || s.queuedMediaAgeLocked(now) > videoSubscriberMaxLatency {
		s.replaceLocked(nil, true)
	}
}

func (s *videoSubscription) queuedMediaAgeLocked(now time.Time) time.Duration {
	for i := s.head; i < len(s.queue); i++ {
		packet := s.queue[i]
		if packet.Config && !packet.Keyframe {
			continue
		}
		if packet.receivedAt.IsZero() {
			return 0
		}
		return now.Sub(packet.receivedAt)
	}
	return 0
}

func (s *videoSubscription) enqueueConfig(packet VideoPacket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.replaceLocked([]VideoPacket{packet}, true)
}

func (s *videoSubscription) enqueueKeyframe(config *VideoPacket, packet VideoPacket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}

	if s.waitingForKeyframe || s.head < len(s.queue) {
		replay := keyframeReplay(config, packet)
		s.replaceLocked(replay, false)
		return
	}

	s.waitingForKeyframe = false
	s.appendLocked(packet)
}

func (s *videoSubscription) appendLocked(packet VideoPacket) {
	wasEmpty := s.head == len(s.queue)
	s.queue = append(s.queue, packet)
	s.queuedBytes += len(packet.Data)
	if wasEmpty {
		s.signalLocked()
	}
}

func (s *videoSubscription) replaceLocked(packets []VideoPacket, waitingForKeyframe bool) {
	for i := s.head; i < len(s.queue); i++ {
		s.queue[i] = VideoPacket{}
	}
	s.queue = append(s.queue[:0], packets...)
	s.head = 0
	s.queuedBytes = 0
	for i := range packets {
		s.queuedBytes += len(packets[i].Data)
	}
	s.waitingForKeyframe = waitingForKeyframe
	if len(packets) > 0 || s.closed {
		s.signalLocked()
	}
}

func (s *videoSubscription) signalLocked() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

func (s *videoSubscription) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.replaceLocked(nil, s.waitingForKeyframe)
}

type videoBroadcaster struct {
	mu              sync.Mutex
	subscribers     map[*videoSubscription]struct{}
	latestConfig    *VideoPacket
	currentGOP      []VideoPacket
	currentGOPBytes int
	closed          bool
}

func newVideoBroadcaster() *videoBroadcaster {
	return &videoBroadcaster{
		subscribers: make(map[*videoSubscription]struct{}),
	}
}

func (b *videoBroadcaster) Subscribe() (*videoSubscription, func()) {
	b.mu.Lock()
	initial := b.replayPacketsLocked()
	waitingForKeyframe := true
	for i := range initial {
		if initial[i].Keyframe {
			waitingForKeyframe = false
			break
		}
	}
	subscription := newVideoSubscription(initial, waitingForKeyframe)
	if b.closed {
		subscription.close()
	} else {
		b.subscribers[subscription] = struct{}{}
	}
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		delete(b.subscribers, subscription)
		subscription.close()
		b.mu.Unlock()
	}

	return subscription, unsubscribe
}

func (b *videoBroadcaster) broadcast(packet VideoPacket) {
	b.broadcastAt(packet, time.Now())
}

func (b *videoBroadcaster) broadcastAt(packet VideoPacket, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	packet.receivedAt = now

	if packet.Config {
		nextConfig := videoConfigPacket(packet)
		configChanged := b.latestConfig == nil || !bytes.Equal(b.latestConfig.Data, nextConfig.Data)
		if configChanged {
			b.latestConfig = nextConfig
		}
		if !packet.Keyframe {
			if !configChanged {
				return
			}
			b.currentGOP = nil
			b.currentGOPBytes = 0
			for subscription := range b.subscribers {
				subscription.enqueueConfig(*b.latestConfig)
			}
			return
		}
	}
	if packet.Keyframe {
		if len(packet.Data) <= videoReplayMaxQueuedBytes {
			b.currentGOP = []VideoPacket{packet}
			b.currentGOPBytes = len(packet.Data)
		} else {
			b.currentGOP = nil
			b.currentGOPBytes = 0
		}
		for subscription := range b.subscribers {
			subscription.enqueueKeyframe(b.latestConfig, packet)
		}
		return
	}
	if len(b.currentGOP) > 0 {
		if b.currentGOPBytes+len(packet.Data) <= videoReplayMaxQueuedBytes {
			b.currentGOP = append(b.currentGOP, packet)
			b.currentGOPBytes += len(packet.Data)
		} else {
			b.currentGOP = nil
			b.currentGOPBytes = 0
		}
	}
	for subscription := range b.subscribers {
		subscription.enqueueDeltaAt(packet, now)
	}
}

func (b *videoBroadcaster) replayPacketsLocked() []VideoPacket {
	if len(b.currentGOP) == 0 {
		if b.latestConfig == nil {
			return nil
		}
		return []VideoPacket{*b.latestConfig}
	}

	packets := make([]VideoPacket, 0, len(b.currentGOP)+1)
	if !b.currentGOP[0].Config && b.latestConfig != nil {
		packets = append(packets, *b.latestConfig)
	}
	packets = append(packets, b.currentGOP...)
	return packets
}

func keyframeReplay(config *VideoPacket, keyframe VideoPacket) []VideoPacket {
	if keyframe.Config || config == nil {
		return []VideoPacket{keyframe}
	}
	replayConfig := *config
	replayConfig.receivedAt = keyframe.receivedAt
	return []VideoPacket{replayConfig, keyframe}
}

func videoConfigPacket(packet VideoPacket) *VideoPacket {
	config := packet
	if configData := h264ConfigData(packet.Data); len(configData) > 0 {
		config.Data = configData
		config.Config = true
		config.Keyframe = false
		config.encoded = nil
		encodeVideoPacket(&config)
	}
	return &config
}

func (b *videoBroadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for subscription := range b.subscribers {
		subscription.close()
		delete(b.subscribers, subscription)
	}
	b.latestConfig = nil
	b.currentGOP = nil
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
		s.videoBroadcaster.Close()
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

func (s *StreamSession) SubscribeVideo() (*videoSubscription, func(), error) {
	if s.videoBroadcaster == nil {
		return nil, nil, errors.New("video broadcaster not available")
	}

	ch, unsubscribe := s.videoBroadcaster.Subscribe()
	return ch, unsubscribe, nil
}
