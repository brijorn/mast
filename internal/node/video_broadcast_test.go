package node

import (
	"encoding/binary"
	"testing"
	"time"
)

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
