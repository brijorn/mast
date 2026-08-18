package node

import (
	"testing"
	"time"
)

func TestBroadcasterIsIdleUntilSomeoneSubscribes(t *testing.T) {
	start := time.Now()
	b := newVideoBroadcasterAt(start)

	at, idle := b.IdleSince()
	if !idle {
		t.Fatal("a broadcaster nobody has ever watched should read as idle")
	}
	if !at.Equal(start) {
		t.Fatalf("idle since %v, want %v", at, start)
	}
}

func TestBroadcasterIsNotIdleWhileWatched(t *testing.T) {
	b := newVideoBroadcasterAt(time.Now())
	_, unsubscribe := b.Subscribe()

	if _, idle := b.IdleSince(); idle {
		t.Fatal("a stream with a subscriber must never be reaped")
	}

	unsubscribe()
	if _, idle := b.IdleSince(); !idle {
		t.Fatal("the stream should be idle once its last viewer leaves")
	}
}

// The clock starts when the *last* viewer leaves, not when the first arrived,
// so a long viewing session is not reaped the moment it ends.
func TestIdleClockRestartsWhenTheLastViewerLeaves(t *testing.T) {
	b := newVideoBroadcasterAt(time.Now().Add(-time.Hour))

	_, unsubscribe := b.Subscribe()
	unsubscribe()

	at, idle := b.IdleSince()
	if !idle {
		t.Fatal("want idle after the viewer left")
	}
	if time.Since(at) > time.Minute {
		t.Fatalf("idle clock kept the pre-subscribe time %v; it should restart on release", at)
	}
}

func TestSecondViewerKeepsTheStreamAlive(t *testing.T) {
	b := newVideoBroadcasterAt(time.Now())
	_, first := b.Subscribe()
	_, second := b.Subscribe()

	first()
	if _, idle := b.IdleSince(); idle {
		t.Fatal("one viewer leaving must not idle a stream another is still watching")
	}
	second()
	if _, idle := b.IdleSince(); !idle {
		t.Fatal("want idle once both have gone")
	}
}

func TestReapIdleStreamsIgnoresMJPEGAndWatchedStreams(t *testing.T) {
	n := &Node{streams: map[string]*streamEntry{}}
	now := time.Now()
	old := now.Add(-time.Hour)

	watched := &StreamSession{Kind: "h264", videoBroadcaster: newVideoBroadcasterAt(old)}
	_, keep := watched.videoBroadcaster.Subscribe()
	defer keep()

	// iOS serves MJPEG without subscribing, so zero subscribers there says
	// nothing about whether anyone is looking.
	n.streams["mjpeg-device"] = &streamEntry{Session: &StreamSession{Kind: "mjpeg", videoBroadcaster: newVideoBroadcasterAt(old)}, Done: make(chan struct{})}
	n.streams["watched"] = &streamEntry{Session: watched, Done: make(chan struct{})}
	n.streams["abandoned"] = &streamEntry{Session: &StreamSession{Kind: "h264", videoBroadcaster: newVideoBroadcasterAt(old)}, Done: make(chan struct{})}

	stale := n.idleStreams(now, 5*time.Minute)

	if len(stale) != 1 || stale[0] != "abandoned" {
		t.Fatalf("want only the abandoned h264 stream, got %v", stale)
	}
}

func TestIdleStreamsRespectsTheTimeout(t *testing.T) {
	n := &Node{streams: map[string]*streamEntry{}}
	now := time.Now()
	n.streams["recent"] = &streamEntry{
		Session: &StreamSession{Kind: "h264", videoBroadcaster: newVideoBroadcasterAt(now.Add(-time.Minute))},
		Done:    make(chan struct{}),
	}

	if stale := n.idleStreams(now, 5*time.Minute); len(stale) != 0 {
		t.Fatalf("a stream idle for a minute is not stale under a five minute timeout, got %v", stale)
	}
}

func TestReapIdleStreamsDisabledByZeroTimeout(t *testing.T) {
	n := &Node{streams: map[string]*streamEntry{}}
	n.streams["abandoned"] = &streamEntry{
		Session: &StreamSession{Kind: "h264", videoBroadcaster: newVideoBroadcasterAt(time.Now().Add(-time.Hour))},
		Done:    make(chan struct{}),
	}

	if stopped := n.reapIdleStreams(time.Now(), 0); stopped != nil {
		t.Fatalf("zero timeout disables reaping, got %v", stopped)
	}
}
