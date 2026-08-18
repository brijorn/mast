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
// So idleness is decided by the broadcaster's subscriber count rather than by
// the client remembering to clean up. A viewer that comes back simply starts a
// new stream.
//
// Only h264 sessions are considered. The MJPEG path serves iOS through the
// session directly without subscribing to the broadcaster, so a subscriber
// count of zero would not mean nobody is watching.

const idleStreamSweepInterval = 30 * time.Second

// idleStreams names the streams that have had no viewer for at least timeout.
func (n *Node) idleStreams(now time.Time, timeout time.Duration) []string {
	n.streamsMu.RLock()
	defer n.streamsMu.RUnlock()

	var stale []string
	for serial, entry := range n.streams {
		session := entry.Session
		if session == nil || session.Kind != "h264" || session.videoBroadcaster == nil {
			continue
		}
		// A stream still starting up has not had the chance to be watched.
		select {
		case <-entry.Done:
			continue
		default:
		}
		since, idle := session.videoBroadcaster.IdleSince()
		if !idle || now.Sub(since) < timeout {
			continue
		}
		stale = append(stale, serial)
	}
	return stale
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
		log.Printf("stopped stream for %s: no viewer for %s", serial, timeout)
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
