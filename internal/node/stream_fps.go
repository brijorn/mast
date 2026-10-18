package node

import (
	"fmt"
	"log"
	"time"
)

// Frame-rate tiers for viewer streams.
//
// A fleet board shows every phone at once and paints each tile a couple of
// times a second, so it asks for streams capped at that rate (max_fps), while
// someone driving one phone needs it at full rate. One device has one stream,
// so the stream runs at the highest rate any current viewer asks for: a
// request for more than the stream encodes replaces it (ensureStream), and a
// stream encoding more than every viewer still watching it wants is restarted
// lower once that has held for fpsDowngradeGrace. The grace absorbs a driver
// stepping back to the board and straight into the phone again.
//
// A viewer that names no rate wants full rate, so older clients and the iOS
// app are never downgraded.

const fpsDowngradeGrace = time.Minute

type streamViewer struct {
	maxFPS int
}

// addViewer records a video viewer wanting maxFPS (zero for full rate) and
// returns its release.
func (s *StreamSession) addViewer(maxFPS int) func() {
	viewer := &streamViewer{maxFPS: maxFPS}
	s.viewersMu.Lock()
	if s.viewers == nil {
		s.viewers = make(map[*streamViewer]struct{})
	}
	s.viewers[viewer] = struct{}{}
	s.viewersMu.Unlock()
	return func() {
		s.viewersMu.Lock()
		delete(s.viewers, viewer)
		s.viewersMu.Unlock()
	}
}

// wantedFPS reports the highest rate a current viewer wants (zero for full
// rate) and how many viewers there are.
func (s *StreamSession) wantedFPS() (int, int) {
	s.viewersMu.Lock()
	defer s.viewersMu.Unlock()
	wanted := -1
	for viewer := range s.viewers {
		if viewer.maxFPS == 0 {
			return 0, len(s.viewers)
		}
		wanted = max(wanted, viewer.maxFPS)
	}
	return max(wanted, 0), len(s.viewers)
}

// overServedSinceAt advances the session's over-served clock: it starts the
// first time the session is seen encoding more than its viewers want and
// resets whenever it is not.
func (s *StreamSession) overServedSinceAt(now time.Time, over bool) time.Time {
	s.viewersMu.Lock()
	defer s.viewersMu.Unlock()
	if !over {
		s.overServedSince = time.Time{}
	} else if s.overServedSince.IsZero() {
		s.overServedSince = now
	}
	return s.overServedSince
}

// fpsBelow reports whether a stream encoding have frames a second (zero for
// full rate) falls short of a request for want.
func fpsBelow(have, want int) bool {
	return have > 0 && (want == 0 || want > have)
}

func fpsLabel(fps int) string {
	if fps == 0 {
		return "full rate"
	}
	return fmt.Sprintf("%d fps", fps)
}

// overServedStreams names the video streams whose viewers have all wanted a
// lower rate than they encode for at least grace.
func (n *Node) overServedStreams(now time.Time, grace time.Duration) []string {
	n.streamsMu.RLock()
	defer n.streamsMu.RUnlock()
	var stale []string
	for serial, entry := range n.streams {
		session := entry.Session
		if session == nil || session.Kind != "h264" || session.videoBroadcaster == nil {
			continue
		}
		wanted, viewers := session.wantedFPS()
		// No viewers is the idle reaper's question, not this one.
		over := viewers > 0 && fpsBelow(wanted, session.maxFPS)
		since := session.overServedSinceAt(now, over)
		if over && now.Sub(since) >= grace {
			stale = append(stale, serial)
		}
	}
	return stale
}

// reapOverServedStreams stops each stream encoding more than its viewers want.
// Each viewer is told the stream is gone and starts it again at its own rate.
func (n *Node) reapOverServedStreams(now time.Time) []string {
	var stopped []string
	for _, serial := range n.overServedStreams(now, fpsDowngradeGrace) {
		if err := n.stopLocalStream(serial); err != nil {
			log.Printf("stop over-served stream %s: %v", serial, err)
			continue
		}
		log.Printf("stopped stream for %s: its viewers want a lower frame rate", serial)
		stopped = append(stopped, serial)
	}
	return stopped
}
