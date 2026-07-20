package node

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestReadVideoPacketSkipsScrcpySessionResize(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payload := annexBNAL(1, 9)
	stream := &StreamSession{videoConn: client, Width: 498, Height: 1080}
	go writeScrcpyVideoTestData(t, server,
		scrcpySessionHeader(1080, 498),
		scrcpyMediaPacket(scrcpyPacketFlagKeyFrame|42, payload),
	)

	packet, err := stream.readVideoPacket()
	if err != nil {
		t.Fatalf("readVideoPacket() error = %v", err)
	}
	if packet.PTS != 42 || packet.Config || !packet.Keyframe || !bytes.Equal(packet.Data, payload) {
		t.Fatalf("packet = %+v, want PTS 42 keyframe with unmodified payload", packet)
	}
	if width, height := stream.Dimensions(); width != 1080 || height != 498 {
		t.Fatalf("stream dimensions = %dx%d, want 1080x498", width, height)
	}
}

func TestReadVideoPacketUsesScrcpyV4ConfigFlag(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payload := annexBNAL(1, 7)
	stream := &StreamSession{videoConn: client}
	go writeScrcpyVideoTestData(t, server,
		scrcpyMediaPacket(scrcpyPacketFlagConfig, payload),
	)

	packet, err := stream.readVideoPacket()
	if err != nil {
		t.Fatalf("readVideoPacket() error = %v", err)
	}
	if !packet.Config || packet.Keyframe || packet.PTS != 0 || !bytes.Equal(packet.Data, payload) {
		t.Fatalf("packet = %+v, want config packet with unmodified payload", packet)
	}
}

func TestReadVideoPacketRejectsInvalidScrcpySessionDimensions(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	stream := &StreamSession{videoConn: client}
	go writeScrcpyVideoTestData(t, server, scrcpySessionHeader(1080, 0))

	if _, err := stream.readVideoPacket(); err == nil {
		t.Fatal("readVideoPacket() error = nil, want invalid session dimensions error")
	}
}

func TestVideoBroadcasterReplaysCompleteCurrentGOP(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)})
	broadcaster.broadcast(VideoPacket{PTS: 3, Data: annexBNAL(1, 3)})
	broadcaster.broadcast(VideoPacket{PTS: 4, Data: annexBNAL(1, 4)})

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	got := queuedVideoPackets(subscription)
	assertVideoPTS(t, got, 1, 2, 3, 4)
	if !got[0].Config || !got[1].Keyframe {
		t.Fatalf("replay flags = config:%t keyframe:%t, want true true", got[0].Config, got[1].Keyframe)
	}
}

func TestVideoBroadcasterDoesNotReplayFramesBeforeLatestKeyframe(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)})
	broadcaster.broadcast(VideoPacket{PTS: 3, Data: annexBNAL(1, 3)})
	broadcaster.broadcast(VideoPacket{PTS: 4, Keyframe: true, Data: annexBNAL(5, 4)})
	broadcaster.broadcast(VideoPacket{PTS: 5, Data: annexBNAL(1, 5)})

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	assertVideoPTS(t, queuedVideoPackets(subscription), 1, 4, 5)
}

func TestVideoBroadcasterReplaysCombinedConfigKeyframeOnce(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	configKeyframe := append(append(annexBNAL(7, 1), annexBNAL(8, 2)...), annexBNAL(5, 3)...)
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Keyframe: true, Data: configKeyframe})
	broadcaster.broadcast(VideoPacket{PTS: 2, Data: annexBNAL(1, 4)})

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	got := queuedVideoPackets(subscription)
	assertVideoPTS(t, got, 1, 2)
	if !got[0].Config || !got[0].Keyframe {
		t.Fatalf("first replay packet = %+v, want combined config keyframe", got[0])
	}
}

func TestVideoBroadcasterWaitsForKeyframeAfterConfig(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)})

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()
	assertVideoPTS(t, takeVideoPackets(t, subscription, 1), 1)

	broadcaster.broadcast(VideoPacket{PTS: 2, Data: annexBNAL(1, 2)})
	if got := queuedVideoPackets(subscription); len(got) != 0 {
		t.Fatalf("queued packets before keyframe = %+v, want none", got)
	}

	broadcaster.broadcast(VideoPacket{PTS: 3, Keyframe: true, Data: annexBNAL(5, 3)})
	broadcaster.broadcast(VideoPacket{PTS: 4, Data: annexBNAL(1, 4)})
	assertVideoPTS(t, queuedVideoPackets(subscription), 1, 3, 4)
}

func TestVideoBroadcasterResyncsLaggingSubscriberAtKeyframe(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)})

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()
	takeVideoPackets(t, subscription, 2)

	broadcaster.broadcast(VideoPacket{PTS: 3, Data: annexBNAL(1, 3)})
	broadcaster.broadcast(VideoPacket{PTS: 4, Data: annexBNAL(1, 4)})
	broadcaster.broadcast(VideoPacket{PTS: 5, Keyframe: true, Data: annexBNAL(5, 5)})
	broadcaster.broadcast(VideoPacket{PTS: 6, Data: annexBNAL(1, 6)})

	got := queuedVideoPackets(subscription)
	assertVideoPTS(t, got, 1, 5, 6)
	if !got[0].Config || !got[1].Keyframe {
		t.Fatalf("resync flags = config:%t keyframe:%t, want true true", got[0].Config, got[1].Keyframe)
	}
}

func TestVideoBroadcasterBoundsGOPWithoutKeyframes(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)})

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()
	takeVideoPackets(t, subscription, 2)

	broadcaster.broadcast(VideoPacket{
		PTS:  3,
		Data: make([]byte, videoSubscriberMaxQueuedBytes+1),
	})
	broadcaster.broadcast(VideoPacket{PTS: 4, Data: annexBNAL(1, 4)})
	if got := queuedVideoPackets(subscription); len(got) != 0 {
		t.Fatalf("queued packets after hard limit = %d, want 0", len(got))
	}

	broadcaster.broadcast(VideoPacket{PTS: 5, Keyframe: true, Data: annexBNAL(5, 5)})
	assertVideoPTS(t, queuedVideoPackets(subscription), 1, 5)
}

func TestVideoSubscriptionDropsPacketsPastLatencyBudget(t *testing.T) {
	startedAt := time.Unix(1_000, 0)
	subscription := newVideoSubscription(nil, false)

	subscription.enqueueDeltaAt(VideoPacket{
		PTS:        1,
		Data:       annexBNAL(1, 1),
		receivedAt: startedAt,
	}, startedAt)
	subscription.enqueueDeltaAt(VideoPacket{
		PTS:        2,
		Data:       annexBNAL(1, 2),
		receivedAt: startedAt.Add(videoSubscriberMaxLatency + time.Millisecond),
	}, startedAt.Add(videoSubscriberMaxLatency+time.Millisecond))

	if got := queuedVideoPackets(subscription); len(got) != 0 {
		t.Fatalf("queued packets after latency limit = %+v, want none", got)
	}
	if !subscription.waitingForKeyframe {
		t.Fatal("waitingForKeyframe = false, want true after dropping stale queue")
	}
}

func TestVideoSubscriptionLatencyTracksCurrentQueueHead(t *testing.T) {
	startedAt := time.Unix(2_000, 0)
	subscription := newVideoSubscription(nil, false)
	subscription.enqueueDeltaAt(VideoPacket{
		PTS:        1,
		Data:       annexBNAL(1, 1),
		receivedAt: startedAt,
	}, startedAt)
	subscription.enqueueDeltaAt(VideoPacket{
		PTS:        2,
		Data:       annexBNAL(1, 2),
		receivedAt: startedAt.Add(400 * time.Millisecond),
	}, startedAt.Add(400*time.Millisecond))

	assertVideoPTS(t, takeVideoPackets(t, subscription, 1), 1)
	subscription.enqueueDeltaAt(VideoPacket{
		PTS:        3,
		Data:       annexBNAL(1, 3),
		receivedAt: startedAt.Add(600 * time.Millisecond),
	}, startedAt.Add(600*time.Millisecond))

	assertVideoPTS(t, queuedVideoPackets(subscription), 2, 3)
}

func TestVideoBroadcasterReplaysStaticGOPAfterWallClockDelay(t *testing.T) {
	startedAt := time.Unix(3_000, 0)
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcastAt(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)}, startedAt)
	broadcaster.broadcastAt(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)}, startedAt)
	broadcaster.broadcastAt(VideoPacket{PTS: 3, Data: annexBNAL(1, 3)}, startedAt.Add(100*time.Millisecond))

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	assertVideoPTS(t, queuedVideoPackets(subscription), 1, 2, 3)
	if subscription.waitingForKeyframe {
		t.Fatal("waitingForKeyframe = true, want the last static frame to remain replayable")
	}
}

func TestVideoBroadcasterKeepsStaticGOPAfterDuplicateConfig(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	config := annexBNAL(7, 1)
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: config})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)})
	broadcaster.broadcast(VideoPacket{PTS: 3, Data: annexBNAL(1, 3)})

	current, unsubscribeCurrent := broadcaster.Subscribe()
	defer unsubscribeCurrent()
	takeVideoPackets(t, current, 3)

	broadcaster.broadcast(VideoPacket{PTS: 4, Config: true, Data: append([]byte(nil), config...)})
	if got := queuedVideoPackets(current); len(got) != 0 {
		t.Fatalf("duplicate config queued for current viewer = %+v, want none", got)
	}
	if current.waitingForKeyframe {
		t.Fatal("current viewer is waiting for a keyframe after duplicate config")
	}

	late, unsubscribeLate := broadcaster.Subscribe()
	defer unsubscribeLate()
	assertVideoPTS(t, queuedVideoPackets(late), 1, 2, 3)
	if late.waitingForKeyframe {
		t.Fatal("late viewer is waiting for a keyframe after duplicate config")
	}
}

func TestVideoBroadcasterInvalidatesStaticGOPAfterChangedConfig(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)})
	broadcaster.broadcast(VideoPacket{PTS: 3, Config: true, Data: annexBNAL(7, 3)})

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()
	assertVideoPTS(t, queuedVideoPackets(subscription), 3)
	if !subscription.waitingForKeyframe {
		t.Fatal("viewer is not waiting for a keyframe after codec config changed")
	}
}

func TestVideoBroadcasterKeepsSparseGOPAcrossWallClockGap(t *testing.T) {
	startedAt := time.Unix(4_000, 0)
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcastAt(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)}, startedAt)
	broadcaster.broadcastAt(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)}, startedAt)
	broadcaster.broadcastAt(
		VideoPacket{PTS: 3, Data: annexBNAL(1, 3)},
		startedAt.Add(time.Hour),
	)

	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	assertVideoPTS(t, queuedVideoPackets(subscription), 1, 2, 3)
	if subscription.waitingForKeyframe {
		t.Fatal("waitingForKeyframe = true after a sparse frame that remained within the byte budget")
	}
}

func TestVideoBroadcasterBoundsRetainedGOPWithoutBlockingCurrentViewer(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: annexBNAL(7, 1)})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: annexBNAL(5, 2)})

	current, unsubscribeCurrent := broadcaster.Subscribe()
	defer unsubscribeCurrent()
	takeVideoPackets(t, current, 2)

	packetSize := videoReplayMaxQueuedBytes/4 + 1
	for pts := uint64(3); pts <= 6; pts++ {
		broadcaster.broadcast(VideoPacket{PTS: pts, Data: make([]byte, packetSize)})
		assertVideoPTS(t, takeVideoPackets(t, current, 1), pts)
	}

	late, unsubscribeLate := broadcaster.Subscribe()
	defer unsubscribeLate()
	assertVideoPTS(t, takeVideoPackets(t, late, 1), 1)

	broadcaster.broadcast(VideoPacket{PTS: 7, Data: annexBNAL(1, 7)})
	assertVideoPTS(t, takeVideoPackets(t, current, 1), 7)
	if got := queuedVideoPackets(late); len(got) != 0 {
		t.Fatalf("late viewer received deltas without a retained keyframe: %+v", got)
	}
}

func TestVideoBroadcasterCloseUnblocksSubscriber(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	subscription, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	done := make(chan bool, 1)
	go func() {
		_, ok := subscription.Next()
		done <- ok
	}()

	broadcaster.Close()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("Next returned a packet after broadcaster close")
		}
	case <-time.After(time.Second):
		t.Fatal("Next remained blocked after broadcaster close")
	}
}

func TestEncodeVideoPacketReusesOwnedBuffer(t *testing.T) {
	packet := VideoPacket{
		PTS:      42,
		Config:   true,
		Keyframe: true,
		Data:     []byte{1, 2, 3},
	}

	first := encodeVideoPacket(&packet)
	second := encodeVideoPacket(&packet)
	if &first[0] != &second[0] {
		t.Fatal("encoded packet buffer was allocated more than once")
	}
	if first[0] != 3 || binary.BigEndian.Uint64(first[1:9]) != 42 || binary.BigEndian.Uint32(first[9:13]) != 3 {
		t.Fatalf("encoded header = %v, want flags=3 pts=42 size=3", first[:13])
	}
}

func takeVideoPackets(t *testing.T, subscription *videoSubscription, count int) []VideoPacket {
	t.Helper()
	packets := make([]VideoPacket, 0, count)
	for range count {
		packet, ok := subscription.Next()
		if !ok {
			t.Fatalf("subscription closed after %d of %d packets", len(packets), count)
		}
		packets = append(packets, packet)
	}
	return packets
}

func queuedVideoPackets(subscription *videoSubscription) []VideoPacket {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return append([]VideoPacket(nil), subscription.queue[subscription.head:]...)
}

func assertVideoPTS(t *testing.T, packets []VideoPacket, want ...uint64) {
	t.Helper()
	if len(packets) != len(want) {
		t.Fatalf("packet count = %d, want %d; packets = %+v", len(packets), len(want), packets)
	}
	for i := range want {
		if packets[i].PTS != want[i] {
			t.Fatalf("packet %d PTS = %d, want %d; packets = %+v", i, packets[i].PTS, want[i], packets)
		}
	}
}

func annexBNAL(typ byte, payload byte) []byte {
	return []byte{0, 0, 0, 1, typ, payload}
}

func scrcpySessionHeader(width int, height int) []byte {
	header := make([]byte, 12)
	binary.BigEndian.PutUint64(header[:8], scrcpyPacketFlagSession|uint64(uint32(width)))
	binary.BigEndian.PutUint32(header[8:12], uint32(height))
	return header
}

func scrcpyMediaPacket(ptsAndFlags uint64, payload []byte) []byte {
	packet := make([]byte, 12+len(payload))
	binary.BigEndian.PutUint64(packet[:8], ptsAndFlags)
	binary.BigEndian.PutUint32(packet[8:12], uint32(len(payload)))
	copy(packet[12:], payload)
	return packet
}

func writeScrcpyVideoTestData(t *testing.T, conn net.Conn, chunks ...[]byte) {
	t.Helper()
	for _, chunk := range chunks {
		if _, err := conn.Write(chunk); err != nil {
			t.Errorf("write test video data: %v", err)
			return
		}
	}
}
