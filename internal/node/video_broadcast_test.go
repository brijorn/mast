package node

import (
	"testing"
	"time"
)

func TestVideoBroadcasterReplaysFreshConfigAndKeyframe(t *testing.T) {
	broadcaster := newVideoBroadcaster()

	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: []byte{1}})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: []byte{2}})

	packets, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	got := drainVideoPackets(packets)
	if len(got) != 2 {
		t.Fatalf("replay packet count = %d, want 2", len(got))
	}
	if !got[0].Config || got[0].PTS != 1 {
		t.Fatalf("first replay packet = %+v, want config PTS 1", got[0])
	}
	if !got[1].Keyframe || got[1].PTS != 2 {
		t.Fatalf("second replay packet = %+v, want keyframe PTS 2", got[1])
	}
}

func TestVideoBroadcasterSkipsStaleKeyframeReplay(t *testing.T) {
	broadcaster := newVideoBroadcaster()

	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Data: []byte{1}})
	broadcaster.broadcast(VideoPacket{PTS: 2, Keyframe: true, Data: []byte{2}})
	broadcaster.latestKeyframe.receivedAt = time.Now().Add(-videoReplayKeyframeMaxAge - time.Second)

	packets, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	got := drainVideoPackets(packets)
	if len(got) != 1 {
		t.Fatalf("replay packet count = %d, want only config", len(got))
	}
	if !got[0].Config || got[0].Keyframe {
		t.Fatalf("replay packet = %+v, want non-keyframe config only", got[0])
	}
}

func TestVideoBroadcasterDoesNotReplayConfigKeyframeTwice(t *testing.T) {
	broadcaster := newVideoBroadcaster()

	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Keyframe: true, Data: []byte{1}})

	packets, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	got := drainVideoPackets(packets)
	if len(got) != 1 {
		t.Fatalf("replay packet count = %d, want 1", len(got))
	}
	if !got[0].Config || !got[0].Keyframe || got[0].PTS != 1 {
		t.Fatalf("replay packet = %+v, want config keyframe PTS 1", got[0])
	}
}

func TestVideoBroadcasterReplaysExtractedConfigBeforeSamePacketKeyframe(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	configKeyframe := append(append(annexBNAL(7, 1), annexBNAL(8, 2)...), annexBNAL(5, 3)...)

	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Keyframe: true, Data: configKeyframe})

	packets, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	got := drainVideoPackets(packets)
	if len(got) != 2 {
		t.Fatalf("replay packet count = %d, want 2", len(got))
	}
	if !got[0].Config || got[0].Keyframe {
		t.Fatalf("first replay packet = %+v, want extracted config only", got[0])
	}
	if types := h264NALTypes(got[0].Data); len(types) != 2 || types[0] != 7 || types[1] != 8 {
		t.Fatalf("first replay NAL types = %v, want [7 8]", types)
	}
	if !got[1].Keyframe || got[1].PTS != 1 {
		t.Fatalf("second replay packet = %+v, want original keyframe", got[1])
	}
}

func TestVideoBroadcasterReplaysExtractedConfigWhenKeyframeIsStale(t *testing.T) {
	broadcaster := newVideoBroadcaster()
	configKeyframe := append(append(annexBNAL(7, 1), annexBNAL(8, 2)...), annexBNAL(5, 3)...)

	broadcaster.broadcast(VideoPacket{PTS: 1, Config: true, Keyframe: true, Data: configKeyframe})
	broadcaster.latestKeyframe.receivedAt = time.Now().Add(-videoReplayKeyframeMaxAge - time.Second)

	packets, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	got := drainVideoPackets(packets)
	if len(got) != 1 {
		t.Fatalf("replay packet count = %d, want extracted config only", len(got))
	}
	if !got[0].Config || got[0].Keyframe {
		t.Fatalf("replay packet = %+v, want extracted config without stale keyframe", got[0])
	}
	if types := h264NALTypes(got[0].Data); len(types) != 2 || types[0] != 7 || types[1] != 8 {
		t.Fatalf("replay NAL types = %v, want [7 8]", types)
	}
}

func drainVideoPackets(packets <-chan VideoPacket) []VideoPacket {
	var drained []VideoPacket
	for {
		select {
		case packet := <-packets:
			drained = append(drained, packet)
		default:
			return drained
		}
	}
}

func annexBNAL(typ byte, payload byte) []byte {
	return []byte{0, 0, 0, 1, typ, payload}
}
