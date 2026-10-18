package node

import (
	"log"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
)

// Tearing down viewer streams that nobody is watching.
//
// An Android viewer stream is a scrcpy server on the phone: it holds a private
// virtual display open and keeps the hardware encoder running for as long as it
// lives. Runway's phones page starts one when someone looks at a handset and is
// meant to stop it when they leave, but a closed laptop, a crashed tab or a
// dropped tunnel never sends that stop. Streams have been found still encoding
// video a day later with no viewer attached, which costs the phone battery and
// heat it is supposed to be spending on earning, and is the leading suspect for
// the graphics memory these handsets leak until Android kills their node.
//
// So idleness is decided by use the node can see rather than by the client
// remembering to clean up: a viewer subscribed to the video, or a client on the
// control websocket. A program that only injects touches holds a control
// client and no viewer, and must keep its session; a viewer that comes back
// simply starts a new stream.
//
// One exception keeps that rule from pinning an encoder on for a program: a
// video session marked controlWithoutVideo serves control clients that would
// be as well served by an interaction session, so it is idle once no viewer
// watches. Reaping it closes those clients, which reconnect into an
// interaction session.
//
// iOS sessions are not considered. The MJPEG path serves iOS through the
// session directly without subscribing to the broadcaster, so a subscriber
// count of zero would not mean nobody is watching.

const idleStreamSweepInterval = 30 * time.Second

// idleStreams names the streams nobody has used for at least timeout.
func (n *Node) idleStreams(now time.Time, timeout time.Duration) []string {
	n.streamsMu.RLock()
	defer n.streamsMu.RUnlock()

	var stale []string
	for serial, entry := range n.streams {
		// A nil session is a stream still starting, which has not had the
		// chance to be used.
		session := entry.Session
		if session == nil {
			continue
		}
		clients, releasedAt := n.controlClientUse(serial)
		since, idle := sessionIdleSince(session, clients, releasedAt)
		if !idle || now.Sub(since) < timeout {
			continue
		}
		stale = append(stale, serial)
	}
	return stale
}

// sessionIdleSince reports since when nobody has used session, given its
// device's control clients and when the last of them left.
func sessionIdleSince(session *StreamSession, clients int, releasedAt time.Time) (time.Time, bool) {
	var since time.Time
	switch {
	case session.Kind == interactionSessionKind:
		since = session.startedAt
	case session.Kind == "h264" && session.videoBroadcaster != nil:
		var idle bool
		since, idle = session.videoBroadcaster.IdleSince()
		if !idle {
			return time.Time{}, false
		}
		if clients > 0 && session.controlWithoutVideo.Load() {
			return since, true
		}
	default:
		return time.Time{}, false
	}
	if clients > 0 {
		return time.Time{}, false
	}
	if releasedAt.After(since) {
		since = releasedAt
	}
	return since, true
}

// reapIdleStreams stops every stream that has gone unwatched for timeout, and
// reports which ones it stopped. Stopping happens outside the read lock because
// it takes the write lock itself.
func (n *Node) reapIdleStreams(now time.Time, timeout time.Duration) []string {
	if timeout <= 0 {
		return nil
	}
	var stopped []string
	for _, serial := range n.idleStreams(now, timeout) {
		if err := n.stopLocalStream(serial); err != nil {
			log.Printf("stop idle stream %s: %v", serial, err)
			continue
		}
		log.Printf("stopped stream for %s: unused for %s", serial, timeout)
		stopped = append(stopped, serial)
	}
	return stopped
}

// streamIdleTimeout reads the policy per sweep rather than caching it, so
// changing stream_idle_timeout takes effect without restarting the node — the
// same reason StreamDefaults is read per stream start.
func (n *Node) streamIdleTimeout() time.Duration {
	n.configMu.RLock()
	defer n.configMu.RUnlock()
	seconds := mastconfig.DefaultStreamIdleTimeout
	if n.configReady {
		seconds = n.configState.StreamIdleTimeout
	}
	return time.Duration(seconds) * time.Second
}

func (n *Node) monitorIdleStreams() {
	ticker := time.NewTicker(idleStreamSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case now := <-ticker.C:
			n.reapIdleStreams(now, n.streamIdleTimeout())
		}
	}
}
