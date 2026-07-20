package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brijorn/ioslink"
	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/scrcpy"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/brijorn/mast/internal/transport"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type streamEntry struct {
	Session *StreamSession
	Done    chan struct{}
	Error   error
}

// StreamSession is the internal stream lifecycle state.
type StreamSession struct {
	ID           string
	DeviceSerial string
	Platform     string
	Kind         string
	Host         string
	LocalPort    int
	VideoURL     string
	MJPEGURL     string

	streamListener net.Listener

	videoConn        net.Conn
	videoBroadcaster *videoBroadcaster

	controlConn net.Conn
	controlMu   sync.Mutex

	dimensionsMu sync.RWMutex
	Width        int
	Height       int
	cmd          *exec.Cmd

	iosDevice  *ioslink.Device
	iosCleanup func()
	iosTouchMu sync.Mutex
	iosTouch   *iosTouchState
}

// Dimensions returns the current encoded video coordinate space. scrcpy may
// update it while a stream is running after a rotation or display resize.
func (s *StreamSession) Dimensions() (int, int) {
	s.dimensionsMu.RLock()
	defer s.dimensionsMu.RUnlock()
	return s.Width, s.Height
}

func (s *StreamSession) setDimensions(width int, height int) {
	s.dimensionsMu.Lock()
	defer s.dimensionsMu.Unlock()
	s.Width = width
	s.Height = height
}

type iosTouchState struct {
	Points    []iosTouchPoint
	StartedAt time.Time
}

type iosTouchPoint struct {
	X int
	Y int
}

const (
	peerStreamRPCTimeout     = 30 * time.Second
	videoStartupInitialWait  = 500 * time.Millisecond
	videoStartupWakeWait     = 5 * time.Second
	videoWriteTimeout        = 2 * time.Second
	VideoCloseStreamNotFound = 4004
)

var ErrStreamNotFound = errors.New("stream not found")

func streamNotFoundError(serial string) error {
	return fmt.Errorf("%w: %s", ErrStreamNotFound, serial)
}

func IsStreamNotFound(err error) bool {
	return errors.Is(err, ErrStreamNotFound) || (err != nil && strings.Contains(err.Error(), "stream not found"))
}

func (s *StreamSession) Stop() error {
	var err error

	if s.streamListener != nil {
		if closeErr := s.streamListener.Close(); closeErr != nil {
			err = closeErr
		}
	}

	if s.videoConn != nil {
		if closeErr := s.videoConn.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}

	if s.controlConn != nil {
		if closeErr := s.controlConn.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}

	if s.cmd != nil && s.cmd.Process != nil {
		killErr := s.cmd.Process.Kill()
		if killErr != nil && err == nil {
			err = killErr
		}
		if waitErr := s.cmd.Wait(); waitErr != nil && err == nil && killErr != nil {
			err = waitErr
		}
	}

	if s.iosCleanup != nil {
		s.iosCleanup()
	}

	return err
}

func (s *StreamSession) getStderrDiagnostics() string {
	if s.cmd == nil || s.cmd.Stderr == nil {
		return ""
	}
	if sb, ok := s.cmd.Stderr.(interface{ String() string }); ok {
		str := sb.String()
		if str != "" {
			return "\nscrcpy stderr:\n" + str
		}
	}
	return ""
}

func (s *StreamSession) acceptScrcpyConnection(opts streamcfg.Options) error {
	videoConn, err := acceptScrcpySocket(s.streamListener)
	if err != nil {
		return fmt.Errorf("accept scrcpy video socket: %w%s", err, s.getStderrDiagnostics())
	}

	deviceName, width, height, err := readScrcpyVideoMetadata(videoConn)
	if err != nil {
		_ = videoConn.Close()
		return fmt.Errorf("read scrcpy video metadata: %w%s", err, s.getStderrDiagnostics())
	}

	s.setDimensions(width, height)
	s.videoConn = videoConn
	_ = deviceName

	if !opts.NoAudio {
		audioConn, err := acceptScrcpySocket(s.streamListener)
		if err != nil {
			_ = videoConn.Close()
			return fmt.Errorf("accept scrcpy audio socket: %w%s", err, s.getStderrDiagnostics())
		}
		_ = audioConn.Close()
	}

	if !opts.NoControl {
		controlConn, err := acceptScrcpySocket(s.streamListener)
		if err != nil {
			_ = videoConn.Close()
			return fmt.Errorf("accept scrcpy control socket: %w%s", err, s.getStderrDiagnostics())
		}
		s.controlConn = controlConn
	}

	return nil
}

func (s *StreamSession) setDisplayPower(on bool) error {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()

	if s.controlConn == nil {
		return errors.New("display power requires control")
	}
	return scrcpy.WriteSetDisplayPower(s.controlConn, on)
}

func (n *Node) applyStreamOptions(session *StreamSession, opts streamcfg.Options) error {
	if !opts.TurnScreenOff {
		return nil
	}
	if session.Platform == PlatformIOS {
		return nil
	}
	return session.setDisplayPower(false)
}

func waitForVideoKeyframe(ctx context.Context, packets *videoSubscription) error {
	for {
		packet, ok, err := packets.NextContext(ctx, 0)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("video source ended before the first keyframe")
		}
		if packet.Keyframe {
			return nil
		}
	}
}

func waitForInitialVideo(
	ctx context.Context,
	packets *videoSubscription,
	wakeDisplay func() error,
	initialWait time.Duration,
	wakeWait time.Duration,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	initialCtx, cancelInitial := context.WithTimeout(ctx, initialWait)
	err := waitForVideoKeyframe(initialCtx, packets)
	cancelInitial()
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err := wakeDisplay(); err != nil {
		return fmt.Errorf("wake display for video startup: %w", err)
	}

	wakeCtx, cancelWake := context.WithTimeout(ctx, wakeWait)
	defer cancelWake()
	if err := waitForVideoKeyframe(wakeCtx, packets); err != nil {
		return fmt.Errorf("wait for video keyframe after waking display: %w", err)
	}
	return nil
}

func acceptScrcpySocket(ln net.Listener) (net.Conn, error) {
	if deadlineSetter, ok := ln.(interface{ SetDeadline(time.Time) error }); ok {
		if err := deadlineSetter.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return nil, err
		}
		defer func() {
			_ = deadlineSetter.SetDeadline(time.Time{})
		}()
	}

	return ln.Accept()
}

func readScrcpyVideoMetadata(conn net.Conn) (string, int, int, error) {
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", 0, 0, err
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	deviceName := make([]byte, 64)
	if _, err := io.ReadFull(conn, deviceName); err != nil {
		return "", 0, 0, err
	}

	streamMeta := make([]byte, 16)
	if _, err := io.ReadFull(conn, streamMeta); err != nil {
		return "", 0, 0, err
	}

	name := string(bytes.TrimRight(deviceName, "\x00"))
	width := int(binary.BigEndian.Uint32(streamMeta[8:12]))
	height := int(binary.BigEndian.Uint32(streamMeta[12:16]))

	return name, width, height, nil
}

func writeScrcpyServerTempFile() (string, func(), error) {
	tempFile, err := os.CreateTemp("", scrcpy.Filename)
	if err != nil {
		return "", nil, err
	}

	if _, err := tempFile.Write(scrcpy.Server); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempFile.Name())
		return "", nil, err
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempFile.Name())
		return "", nil, err
	}

	cleanup := func() {
		_ = os.Remove(tempFile.Name())
	}
	return tempFile.Name(), cleanup, nil
}

func (n *Node) deviceBySerial(serial string) (DeviceInfo, error) {
	device, err := n.DeviceBySerial(serial)
	if err != nil {
		return DeviceInfo{}, err
	}
	return *device, nil
}

func (n *Node) pushScrcpyServer(host string, serial string) error {
	localPath, cleanup, err := writeScrcpyServerTempFile()
	if err != nil {
		return err
	}
	defer cleanup()

	return n.adb.Push(n.ctx, host, serial, localPath, scrcpy.RemotePath)
}

func newScrcpyListener() (net.Listener, int, error) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, 0, err
	}

	port := ln.Addr().(*net.TCPAddr).Port
	return ln, port, nil
}

func (n *Node) createScrcpyReverse(host string, serial string, port int) error {
	return n.adb.Reverse(n.ctx, host, serial, scrcpy.DeviceSocket, port)
}

func (n *Node) startScrcpyProcess(host string, serial string, opts streamcfg.Options) (*exec.Cmd, error) {
	shellArgs := scrcpyServerArgs(opts)
	return n.adb.StartShell(host, serial, shellArgs...)
}

func scrcpyServerArgs(opts streamcfg.Options) []string {
	shellArgs := []string{
		"CLASSPATH=" + scrcpy.RemotePath,
		"app_process",
		"/",
		"com.genymobile.scrcpy.Server",
		scrcpy.ServerVersion,
	}
	return append(shellArgs, opts.Format()...)
}

func (n *Node) StartStream(serial string, opts streamcfg.Options) (*StreamSession, error) {
	opts = opts.WithDefaults()

	device, err := n.deviceBySerial(serial)
	if err != nil {
		return nil, err
	}

	if device.NodeID != n.ID {
		return n.startPeerStream(n.ctx, device.NodeID, serial, opts)
	}
	if device.Platform == PlatformIOS {
		return n.startLocalIOSStream(serial, opts)
	}
	if device.Platform != PlatformAndroid {
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}

	return n.startLocalAndroidStreamAfterLookup(serial, opts)
}

func (n *Node) startLocalAndroidStream(serial string, opts streamcfg.Options) (*StreamSession, error) {
	return n.startLocalAndroidStreamWithDeviceCheck(serial, opts, true)
}

func (n *Node) startLocalAndroidStreamAfterLookup(serial string, opts streamcfg.Options) (*StreamSession, error) {
	return n.startLocalAndroidStreamWithDeviceCheck(serial, opts, false)
}

func (n *Node) startLocalAndroidStreamWithDeviceCheck(serial string, opts streamcfg.Options, checkDevice bool) (*StreamSession, error) {
	opts = opts.WithDefaults()

	if opts.TurnScreenOff && opts.NoControl {
		return nil, errors.New("turn_screen_off requires control")
	}

	if checkDevice {
		device, err := n.localDeviceBySerial(serial)
		if err != nil {
			return nil, err
		}
		if device.Platform != PlatformAndroid {
			return nil, fmt.Errorf("device %s is %s, not android", serial, device.Platform)
		}
	}

	n.configMu.RLock()
	lockPortrait := n.configReady && n.configState.LockPortrait && !opts.PreserveOrientation
	n.configMu.RUnlock()

	if lockPortrait {
		if _, err := n.adb.Shell(n.ctx, "", serial, "wm", "set-ignore-orientation-request", "-d", "0", "true"); err != nil {
			log.Printf("failed to set ignore orientation request on %s: %v", serial, err)
		}
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "system", "accelerometer_rotation", "0"); err != nil {
			log.Printf("failed to disable accelerometer rotation on %s: %v", serial, err)
		}
		if _, err := n.adb.Shell(n.ctx, "", serial, "settings", "put", "system", "user_rotation", "0"); err != nil {
			log.Printf("failed to set user rotation on %s: %v", serial, err)
		}
	}

	streamHost, err := n.streamHostForNode(n.ID)
	if err != nil {
		return nil, err
	}

	host, err := n.adbHostForNode(n.ID)
	if err != nil {
		return nil, err
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
		Kind:           "h264",
		Host:           streamHost,
		LocalPort:      port,
		VideoURL:       "/api/streams/video?serial=" + serial,
		streamListener: ln,
		cmd:            cmd,
	}

	if err := session.acceptScrcpyConnection(opts); err != nil {
		_ = session.Stop()
		return nil, err
	}

	session.videoBroadcaster = newVideoBroadcaster()
	startupPackets, unsubscribeStartup, err := session.SubscribeVideo()
	if err != nil {
		_ = session.Stop()
		return nil, err
	}
	defer unsubscribeStartup()
	go session.broadcastVideo(func() {
		n.streamsMu.Lock()
		entry, ok := n.streams[serial]
		if ok && entry.Session == session {
			delete(n.streams, serial)
		}
		n.streamsMu.Unlock()
	})

	wakeDisplay := func() error {
		log.Printf("video startup for %s produced no keyframe; waking the display", serial)
		if session.controlConn != nil {
			return session.setDisplayPower(true)
		}
		_, err := n.adb.Shell(n.ctx, host, serial, "input", "keyevent", "KEYCODE_WAKEUP")
		return err
	}
	if err := waitForInitialVideo(
		n.ctx,
		startupPackets,
		wakeDisplay,
		videoStartupInitialWait,
		videoStartupWakeWait,
	); err != nil {
		_ = session.Stop()
		return nil, fmt.Errorf("start video for %s: %w%s", serial, err, session.getStderrDiagnostics())
	}
	if err := n.applyStreamOptions(session, opts); err != nil {
		_ = session.Stop()
		return nil, err
	}

	return session, nil
}

func (n *Node) startPeerStream(ctx context.Context, nodeID string, serial string, opts streamcfg.Options) (*StreamSession, error) {
	opts = opts.WithDefaults()

	ctx, cancel := context.WithTimeout(ctx, peerStreamRPCTimeout)
	defer cancel()

	payload := transport.StartStreamRequestPayload{
		Serial:  serial,
		Options: opts,
	}
	response, err := n.sendPeerRPC(ctx, nodeID, transport.TypeStartStreamRequest, payload)
	if err != nil {
		return nil, err
	}
	if response.messageType != transport.TypeStartStreamResponse {
		return nil, fmt.Errorf("unexpected response type: %s", response.messageType)
	}

	var res transport.StartStreamResponse
	if err := json.Unmarshal(response.data, &res); err != nil {
		return nil, err
	}
	if res.Payload.Error != "" {
		return nil, errors.New(res.Payload.Error)
	}
	if res.Payload.Result == nil {
		return nil, errors.New("start stream response missing result")
	}

	return streamSessionFromPayload(res.Payload.Result), nil
}

func (n *Node) handleStartStreamRequest(peer *PeerConn, req transport.StartStreamRequest) {
	session, err := n.ensureLocalStream(req.Payload.Serial, req.Payload.Options)
	payload := transport.StartStreamResponsePayload{}
	if err != nil {
		payload.Error = err.Error()
	} else {
		payload.Result = streamSessionPayload(session)
	}

	n.writePeerResponse(peer, transport.TypeStartStreamResponse, req.RawMessage, payload)
}

func streamSessionPayload(session *StreamSession) *transport.StartStreamResultPayload {
	if session == nil {
		return nil
	}
	width, height := session.Dimensions()
	return &transport.StartStreamResultPayload{
		ID:        session.ID,
		Serial:    session.DeviceSerial,
		Platform:  session.Platform,
		Kind:      session.Kind,
		Host:      session.Host,
		LocalPort: session.LocalPort,
		VideoURL:  session.VideoURL,
		MJPEGURL:  session.MJPEGURL,
		Width:     width,
		Height:    height,
	}
}

func streamSessionFromPayload(payload *transport.StartStreamResultPayload) *StreamSession {
	if payload == nil {
		return nil
	}
	return &StreamSession{
		ID:           payload.ID,
		DeviceSerial: payload.Serial,
		Platform:     payload.Platform,
		Kind:         payload.Kind,
		Host:         payload.Host,
		LocalPort:    payload.LocalPort,
		VideoURL:     payload.VideoURL,
		MJPEGURL:     payload.MJPEGURL,
		Width:        payload.Width,
		Height:       payload.Height,
	}
}

func (n *Node) EnsureStream(serial string, opts streamcfg.Options) (*StreamSession, error) {
	opts = opts.WithDefaults()

	device, err := n.deviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.NodeID != n.ID {
		return n.startPeerStream(n.ctx, device.NodeID, serial, opts)
	}
	switch device.Platform {
	case PlatformIOS:
		return n.ensureStream(serial, opts, n.startLocalIOSStream)
	case PlatformAndroid:
		return n.ensureStream(serial, opts, n.startLocalAndroidStreamAfterLookup)
	default:
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
}

func (n *Node) ensureLocalStream(serial string, opts streamcfg.Options) (*StreamSession, error) {
	opts = opts.WithDefaults()

	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	switch device.Platform {
	case PlatformIOS:
		return n.ensureStream(serial, opts, n.startLocalIOSStream)
	case PlatformAndroid:
		return n.ensureStream(serial, opts, n.startLocalAndroidStreamAfterLookup)
	default:
		return nil, fmt.Errorf("device %s has unsupported platform %s", serial, device.Platform)
	}
}

func (n *Node) ensureStream(serial string, opts streamcfg.Options, start func(string, streamcfg.Options) (*StreamSession, error)) (*StreamSession, error) {
	for {
		n.streamsMu.Lock()
		entry, ok := n.streams[serial]
		if ok {
			n.streamsMu.Unlock()

			<-entry.Done
			if entry.Error != nil {
				return nil, entry.Error
			}

			if entry.Session == nil {
				return nil, errors.New("internal error: stream session is nil")
			}
			if entry.Session.isUnhealthyIOS() {
				n.streamsMu.Lock()
				current, stillCurrent := n.streams[serial]
				if stillCurrent && current == entry {
					delete(n.streams, serial)
				}
				n.streamsMu.Unlock()
				_ = n.cleanupStreamSession(serial, entry.Session)
				continue
			}
			if err := n.applyStreamOptions(entry.Session, opts); err != nil {
				return nil, err
			}

			return entry.Session, nil
		}

		entry = &streamEntry{
			Done: make(chan struct{}),
		}
		n.streams[serial] = entry
		n.streamsMu.Unlock()

		streamSession, err := start(serial, opts)

		n.streamsMu.Lock()
		if err != nil {
			entry.Error = err
			delete(n.streams, serial)
		} else if streamSession == nil {
			err = errors.New("internal error: stream starter returned nil session")
			entry.Error = err
			delete(n.streams, serial)
		} else {
			entry.Session = streamSession
		}
		n.streamsMu.Unlock()
		close(entry.Done)

		if err != nil {
			return nil, err
		}
		return streamSession, nil
	}
}

func (s *StreamSession) isUnhealthyIOS() bool {
	if s.Platform != PlatformIOS || s.iosDevice == nil {
		return false
	}
	return !s.iosDevice.Status().Ready
}

func (n *Node) GetStream(serial string) (*StreamSession, error) {
	n.streamsMu.RLock()
	entry, ok := n.streams[serial]
	n.streamsMu.RUnlock()
	if !ok {
		return nil, streamNotFoundError(serial)
	}
	<-entry.Done
	if entry.Error != nil {
		return nil, entry.Error
	}
	if entry.Session == nil {
		return nil, errors.New("stream session not found: " + serial)
	}

	return entry.Session, nil
}

func (n *Node) viewerStream(serial string) (*StreamSession, error) {
	stream, err := n.GetStream(serial)
	if err != nil {
		return nil, err
	}
	if stream.isUnhealthyIOS() {
		n.DropStream(serial, stream)
		return nil, streamNotFoundError(serial)
	}
	return stream, nil
}

func (n *Node) StreamMJPEG(ctx context.Context, serial string, w http.ResponseWriter) error {
	device, err := n.deviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID != n.ID {
		return n.proxyPeerMJPEG(ctx, device.NodeID, serial, w)
	}

	stream, err := n.viewerStream(serial)
	if err != nil {
		return err
	}
	if stream.Kind != "mjpeg" {
		return errors.New("active stream is not MJPEG")
	}

	if err := stream.StreamMJPEG(ctx, w); err != nil {
		return n.handleMJPEGStreamError(ctx, serial, stream, err)
	}
	return nil
}

func (n *Node) handleMJPEGStreamError(ctx context.Context, serial string, stream *StreamSession, err error) error {
	if isMJPEGViewerDisconnect(ctx, err) {
		return nil
	}
	n.DropStream(serial, stream)
	return err
}

func isMJPEGViewerDisconnect(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "stream closed") ||
		strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "client disconnected") ||
		strings.Contains(message, "request canceled") ||
		strings.Contains(message, "context canceled")
}

func (n *Node) StreamVideo(ctx context.Context, serial string, conn *websocket.Conn) error {
	device, err := n.deviceBySerial(serial)
	if err != nil {
		return err
	}

	if device.NodeID != n.ID {
		return n.proxyPeerVideo(ctx, device.NodeID, serial, conn)
	}

	stream, err := n.viewerStream(serial)
	if err != nil {
		return err
	}
	if stream.Kind != "h264" {
		return errors.New("active stream is not H.264")
	}

	packets, unsubscribe, err := stream.SubscribeVideo()
	if err != nil {
		return err
	}
	defer unsubscribe()
	streamDone := make(chan struct{})
	defer close(streamDone)
	go func() {
		select {
		case <-ctx.Done():
			unsubscribe()
		case <-streamDone:
		}
	}()

	for {
		packet, ok, err := packets.NextContext(ctx, 0)
		if err != nil {
			return err
		}
		if !ok {
			if ctx.Err() != nil {
				return nil
			}
			return streamNotFoundError(serial)
		}
		if err := conn.SetWriteDeadline(time.Now().Add(videoWriteTimeout)); err != nil {
			return err
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, encodeVideoPacket(&packet)); err != nil {
			return err
		}
	}
}

func (n *Node) proxyPeerVideo(ctx context.Context, nodeID string, serial string, conn *websocket.Conn) error {
	streamURL, err := n.peerVideoURL(nodeID, serial)
	if err != nil {
		return err
	}
	streamURL, err = videoViewerURL(streamURL, "peer-"+uuid.NewString())
	if err != nil {
		return err
	}

	peerConn, _, err := websocket.DefaultDialer.DialContext(ctx, streamURL, nil)
	if err != nil {
		return err
	}
	defer func() { _ = peerConn.Close() }()
	streamDone := make(chan struct{})
	defer close(streamDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = peerConn.Close()
		case <-streamDone:
		}
	}()

	for {
		messageType, data, err := peerConn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, VideoCloseStreamNotFound) {
				return streamNotFoundError(serial)
			}
			return err
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		if err := conn.SetWriteDeadline(time.Now().Add(videoWriteTimeout)); err != nil {
			return err
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			return err
		}
	}
}

func (n *Node) proxyPeerMJPEG(ctx context.Context, nodeID string, serial string, w http.ResponseWriter) error {
	streamURL, err := n.peerMJPEGURL(nodeID, serial)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return err
	}
	res, err := n.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = res.Status
		}
		if res.StatusCode == http.StatusNotFound {
			return streamNotFoundError(serial)
		}
		return fmt.Errorf("peer mjpeg stream returned %s: %s", res.Status, message)
	}

	copyHeaders(w.Header(), res.Header)
	w.WriteHeader(res.StatusCode)
	return copyStreamResponse(w, res.Body)
}

func (n *Node) peerMJPEGURL(nodeID string, serial string) (string, error) {
	peer, ok := n.GetPeer(nodeID)
	if !ok {
		return "", errors.New("peer not found: " + nodeID)
	}

	base, err := peerAPIBaseURL(peer.Addr, peer.APIAddr)
	if err != nil {
		return "", err
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	streamPath := &url.URL{Path: "/api/streams/mjpeg"}
	query := streamPath.Query()
	query.Set("serial", serial)
	streamPath.RawQuery = query.Encode()
	return baseURL.ResolveReference(streamPath).String(), nil
}

func (n *Node) peerVideoURL(nodeID string, serial string) (string, error) {
	peer, ok := n.GetPeer(nodeID)
	if !ok {
		return "", errors.New("peer not found: " + nodeID)
	}

	base, err := peerAPIBaseURL(peer.Addr, peer.APIAddr)
	if err != nil {
		return "", err
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	streamPath := &url.URL{Path: "/api/streams/video"}
	query := streamPath.Query()
	query.Set("serial", serial)
	streamPath.RawQuery = query.Encode()
	return websocketURLFromHTTPURL(baseURL.ResolveReference(streamPath)), nil
}

func websocketURLFromHTTPURL(u *url.URL) string {
	clone := *u
	switch clone.Scheme {
	case "https":
		clone.Scheme = "wss"
	default:
		clone.Scheme = "ws"
	}
	return clone.String()
}

func videoViewerURL(rawURL string, viewer string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("viewer", viewer)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func peerAPIBaseURL(host string, apiAddr string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("peer stream host not configured")
	}
	if parsed, err := url.Parse(host); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		host = parsed.Host
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return "http://" + host, nil
	}

	port, err := apiPort(apiAddr)
	if err != nil {
		return "", err
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func apiPort(apiAddr string) (string, error) {
	apiAddr = strings.TrimSpace(apiAddr)
	if apiAddr == "" {
		apiAddr = mastconfig.DefaultAPIAddr
	}
	if parsed, err := url.Parse(apiAddr); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		if port := parsed.Port(); port != "" {
			return port, nil
		}
		return "", fmt.Errorf("api address %q missing port", apiAddr)
	}
	if _, port, err := net.SplitHostPort(apiAddr); err == nil && port != "" {
		return port, nil
	}
	if strings.HasPrefix(apiAddr, ":") && len(apiAddr) > 1 {
		return strings.TrimPrefix(apiAddr, ":"), nil
	}
	if _, err := strconv.Atoi(apiAddr); err == nil {
		return apiAddr, nil
	}
	if idx := strings.LastIndex(apiAddr, ":"); idx >= 0 && idx+1 < len(apiAddr) {
		port := apiAddr[idx+1:]
		if _, err := strconv.Atoi(port); err == nil {
			return port, nil
		}
	}
	return "", fmt.Errorf("api address %q missing port", apiAddr)
}

func copyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyStreamResponse(w http.ResponseWriter, body io.Reader) error {
	buf := make([]byte, 32*1024)
	flusher, _ := w.(http.Flusher)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func (n *Node) StopStream(serial string) error {
	device, err := n.deviceBySerial(serial)
	if err == nil && device.NodeID != n.ID {
		return n.stopPeerStream(device.NodeID, serial)
	}
	return n.stopLocalStream(serial)
}

func (n *Node) DropStream(serial string, session *StreamSession) {
	n.streamsMu.Lock()
	entry, ok := n.streams[serial]
	if !ok {
		n.streamsMu.Unlock()
		return
	}
	select {
	case <-entry.Done:
	default:
		n.streamsMu.Unlock()
		return
	}
	if entry.Session != session {
		n.streamsMu.Unlock()
		return
	}
	delete(n.streams, serial)
	n.streamsMu.Unlock()

	_ = n.cleanupStreamSession(serial, session)
}

func (n *Node) stopPeerStream(nodeID string, serial string) error {
	payload := transport.StopStreamRequestPayload{Serial: serial}
	return n.sendPeerRequest(nodeID, transport.TypeStopStreamRequest, payload)
}

func (n *Node) stopLocalStream(serial string) error {
	n.streamsMu.Lock()
	entry, ok := n.streams[serial]

	if ok {
		delete(n.streams, serial)
	} else {
		n.streamsMu.Unlock()
		return streamNotFoundError(serial)
	}

	n.streamsMu.Unlock()

	<-entry.Done

	if entry.Error != nil {
		return entry.Error
	}

	return n.cleanupStreamSession(serial, entry.Session)
}

func (n *Node) cleanupStreamSession(serial string, session *StreamSession) error {
	if session.Platform != PlatformIOS {
		return session.Stop()
	}
	go func() {
		if err := session.Stop(); err != nil {
			log.Printf("cleanup iOS stream %s: %v", serial, err)
		}
	}()
	return nil
}

func (n *Node) adbHostForNode(nodeID string) (string, error) {
	if nodeID == n.ID {
		return "", nil
	}

	peer, ok := n.GetPeer(nodeID)
	if !ok {
		return "", errors.New("peer not found: " + nodeID)
	}

	return peer.Addr, nil
}

func (n *Node) streamHostForNode(nodeID string) (string, error) {
	if nodeID == n.ID {
		if n.AdvertiseHost == "" {
			return "", errors.New("advertise host not configured")
		}
		return n.AdvertiseHost, nil
	}

	peer, ok := n.GetPeer(nodeID)
	if !ok {
		return "", errors.New("peer not found: " + nodeID)
	}
	return peer.Addr, nil
}
