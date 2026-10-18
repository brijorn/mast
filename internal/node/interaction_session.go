package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/google/uuid"
)

// Interaction sessions: scrcpy's control socket without video.
//
// FrameKit programs open a stream only to inject timed taps and drags over its
// control channel; they never watch its video. A video session keeps a virtual
// display mirrored and the hardware encoder running for as long as it lives,
// which across a fleet of phones running games is a steady load of heat for
// nothing. An interaction session starts scrcpy with video=false: no display,
// no encoder, only the control socket.
//
// With no video, scrcpy has no video frame to map a touch from, so it injects
// the coordinates it is sent as they are, in the display's own logical space.
// The session therefore advertises the display's current logical size as its
// dimensions, which is also the space of a screenshot, and touches pass
// through unscaled.
//
// A viewer arriving on a device that holds an interaction session needs video
// the session cannot add, so ensureStream replaces it with a video session and
// marks that session controlWithoutVideo; once the viewer leaves, the idle
// reaper ends it and the program's next POST brings back an interaction
// session.

const interactionSessionKind = "control"

func (s *StreamSession) hasVideo() bool {
	return s.Kind != interactionSessionKind
}

// ended reports whether an interaction session's scrcpy process has exited.
func (s *StreamSession) ended() bool {
	if s.exited == nil {
		return false
	}
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

func (n *Node) startLocalAndroidInteractionSession(host, streamHost, serial string, opts streamcfg.Options) (*StreamSession, error) {
	opts.NoAudio = true

	width, height, err := n.androidDisplaySize(host, serial)
	if err != nil {
		return nil, fmt.Errorf("read display size for %s: %w", serial, err)
	}

	if err := n.pushScrcpyServer(host, serial); err != nil {
		return nil, err
	}
	ln, port, err := newScrcpyListener()
	if err != nil {
		return nil, err
	}
	if err := n.createScrcpyReverse(host, serial, port); err != nil {
		_ = ln.Close()
		return nil, err
	}
	cmd, err := n.startScrcpyProcess(host, serial, opts)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	session := &StreamSession{
		ID:             uuid.NewString(),
		DeviceSerial:   serial,
		Platform:       PlatformAndroid,
		Kind:           interactionSessionKind,
		Host:           streamHost,
		LocalPort:      port,
		streamListener: ln,
		cmd:            cmd,
		startedAt:      time.Now(),
		exited:         make(chan struct{}),
	}
	session.setDimensions(width, height)
	go func() {
		if cmd != nil {
			_ = cmd.Wait()
		}
		close(session.exited)
	}()

	controlConn, err := acceptScrcpySocket(ln)
	if err != nil {
		_ = session.Stop()
		return nil, fmt.Errorf("accept scrcpy control socket: %w%s", err, session.getStderrDiagnostics())
	}
	session.controlConn = controlConn

	go n.endInteractionSessionOnExit(serial, session)
	n.applyStreamOptions(session, opts)
	return session, nil
}

// endInteractionSessionOnExit retires the session once its scrcpy process
// exits, as the end of video retires a video session, so the next caller
// starts a fresh one instead of writing into a dead socket.
func (n *Node) endInteractionSessionOnExit(serial string, session *StreamSession) {
	<-session.exited
	n.streamsMu.Lock()
	entry, ok := n.streams[serial]
	removed := ok && entry.Session == session
	if removed {
		delete(n.streams, serial)
	}
	n.streamsMu.Unlock()
	if !removed {
		return
	}
	log.Printf("interaction session for %s ended: scrcpy exited%s", serial, session.getStderrDiagnostics())
	_ = session.Stop()
	n.closeControlClients(serial)
}

func (n *Node) androidDisplaySize(host, serial string) (int, int, error) {
	ctx := n.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := n.adbShell(ctx, host, serial, "dumpsys", "window", "displays")
	if err != nil {
		return 0, 0, err
	}
	return parseAndroidDisplaySize(string(out))
}

var displayCurrentSizePattern = regexp.MustCompile(`\bcur=(\d+)x(\d+)`)

// parseAndroidDisplaySize reads the default display's current logical size —
// rotated, and after any `wm size` override — from `dumpsys window displays`.
// That dump also lists scrcpy's own virtual displays, so the size is taken
// from the mDisplayId=0 section only.
func parseAndroidDisplaySize(dump string) (int, int, error) {
	inDefault := false
	for _, line := range strings.Split(dump, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Display: mDisplayId=") {
			id := strings.Fields(strings.TrimPrefix(trimmed, "Display: mDisplayId="))
			inDefault = len(id) > 0 && id[0] == "0"
			continue
		}
		if !inDefault {
			continue
		}
		if match := displayCurrentSizePattern.FindStringSubmatch(trimmed); match != nil {
			width, _ := strconv.Atoi(match[1])
			height, _ := strconv.Atoi(match[2])
			if width > 0 && height > 0 {
				return width, height, nil
			}
		}
	}
	return 0, 0, errors.New("default display size not found in dumpsys window displays")
}
