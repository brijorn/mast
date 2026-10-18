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

// startedEntry holds session the way ensureStream leaves a stream that has
// finished starting: Done closed and the session set.
func startedEntry(session *StreamSession) *streamEntry {
	done := make(chan struct{})
	close(done)
	return &streamEntry{Session: session, Done: done}
}

func TestIdleStreamsCountsViewersAndControlClientsAsUse(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	video := func(idleSince time.Time) *StreamSession {
		return &StreamSession{Kind: "h264", videoBroadcaster: newVideoBroadcasterAt(idleSince)}
	}
	interaction := func(startedAt time.Time) *StreamSession {
		return &StreamSession{Kind: interactionSessionKind, startedAt: startedAt}
	}
	cases := []struct {
		name    string
		session *StreamSession
		// viewer subscribes to the video; client attaches a control client,
		// and released lets it go again at once.
		viewer, client, released, controlWithoutVideo bool
		want                                          bool
	}{
		{name: "abandoned video", session: video(old), want: true},
		{name: "watched video", session: video(old), viewer: true},
		{name: "video idle under the timeout", session: video(now.Add(-time.Minute))},
		{name: "iOS MJPEG, which never subscribes", session: &StreamSession{Kind: "mjpeg", videoBroadcaster: newVideoBroadcasterAt(old)}},
		{name: "video held by a program's control client", session: video(old), client: true},
		{name: "video a viewer opened over a program's interaction session", session: video(old), client: true, controlWithoutVideo: true, want: true},
		{name: "video whose control client just left", session: video(old), client: true, released: true},
		{name: "interaction session in use", session: interaction(old), client: true},
		{name: "interaction session nobody uses", session: interaction(old), want: true},
		{name: "interaction session whose client just left", session: interaction(old), client: true, released: true},
		{name: "interaction session just started", session: interaction(now.Add(-time.Minute))},
	}
	for _, tc := range cases {
		n := &Node{streams: map[string]*streamEntry{}}
		n.streams["phone"] = startedEntry(tc.session)
		if tc.viewer {
			_, unsubscribe := tc.session.videoBroadcaster.Subscribe()
			defer unsubscribe()
		}
		if tc.client {
			release := n.AttachControlClient("phone", func() {})
			if tc.released {
				release()
			}
		}
		tc.session.controlWithoutVideo.Store(tc.controlWithoutVideo)

		stale := n.idleStreams(now.Add(time.Second), 5*time.Minute)

		if got := len(stale) == 1; got != tc.want {
			t.Errorf("%s: idle = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReapIdleStreamsDisabledByZeroTimeout(t *testing.T) {
	n := &Node{streams: map[string]*streamEntry{}}
	n.streams["abandoned"] = startedEntry(&StreamSession{Kind: "h264", videoBroadcaster: newVideoBroadcasterAt(time.Now().Add(-time.Hour))})

	if stopped := n.reapIdleStreams(time.Now(), 0); stopped != nil {
		t.Fatalf("zero timeout disables reaping, got %v", stopped)
	}
}
