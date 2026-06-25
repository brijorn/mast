package node

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/brijorn/mast/internal/scrcpy"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/google/uuid"
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
	Host         string
	LocalPort    int

	streamListener net.Listener

	videoConn        net.Conn
	videoBroadcaster *videoBroadcaster

	controlConn net.Conn

	Width  int
	Height int
	cmd    *exec.Cmd
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
		if killErr := s.cmd.Process.Kill(); killErr != nil && err == nil {
			err = killErr
		}
		if waitErr := s.cmd.Wait(); waitErr != nil && err == nil {
			err = waitErr
		}
	}

	return err
}

func (s *StreamSession) acceptScrcpyConnection(opts streamcfg.Options) error {
	videoConn, err := acceptScrcpySocket(s.streamListener)
	if err != nil {
		return err
	}

	deviceName, width, height, err := readScrcpyVideoMetadata(videoConn)
	if err != nil {
		_ = videoConn.Close()
		return err
	}

	s.Width = width
	s.Height = height
	s.videoConn = videoConn
	_ = deviceName

	if !opts.NoAudio {
		audioConn, err := acceptScrcpySocket(s.streamListener)
		if err != nil {
			_ = videoConn.Close()
			return err
		}
		_ = audioConn.Close()
	}

	if !opts.NoControl {
		controlConn, err := acceptScrcpySocket(s.streamListener)
		if err != nil {
			_ = videoConn.Close()
			return err
		}
		s.controlConn = controlConn
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
	devices, err := n.ListDevices()
	if err != nil {
		return DeviceInfo{}, err
	}

	index := slices.IndexFunc(devices, func(d DeviceInfo) bool {
		return d.Serial == serial
	})
	if index == -1 {
		return DeviceInfo{}, errors.New("device not found: " + serial)
	}

	return devices[index], nil
}

func (n *Node) pushScrcpyServer(host string) error {
	localPath, cleanup, err := writeScrcpyServerTempFile()
	if err != nil {
		return err
	}
	defer cleanup()

	return n.adb.Push(host, localPath, scrcpy.RemotePath)
}

func newScrcpyListener() (net.Listener, int, error) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, 0, err
	}

	port := ln.Addr().(*net.TCPAddr).Port
	return ln, port, nil
}

func (n *Node) createScrcpyReverse(host string, port int) error {
	return n.adb.Reverse(host, scrcpy.DeviceSocket, port)
}

func (n *Node) startScrcpyProcess(host string, opts streamcfg.Options) (*exec.Cmd, error) {
	shellArgs := scrcpyServerArgs(opts)
	return n.adb.StartShell(host, shellArgs...)
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
	device, err := n.deviceBySerial(serial)
	if err != nil {
		return nil, err
	}

	streamHost, err := n.streamHostForNode(device.NodeID)
	if err != nil {
		return nil, err
	}

	host, err := n.adbHostForNode(device.NodeID)
	if err != nil {
		return nil, err
	}

	if err := n.pushScrcpyServer(host); err != nil {
		return nil, err
	}

	ln, port, err := newScrcpyListener()
	if err != nil {
		return nil, err
	}

	if err := n.createScrcpyReverse(host, port); err != nil {
		_ = ln.Close()
		return nil, err
	}

	cmd, err := n.startScrcpyProcess(host, opts)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	session := &StreamSession{
		ID:             uuid.NewString(),
		DeviceSerial:   serial,
		Host:           streamHost,
		LocalPort:      port,
		streamListener: ln,
		cmd:            cmd,
	}

	if err := session.acceptScrcpyConnection(opts); err != nil {
		_ = session.Stop()
		return nil, err
	}

	session.videoBroadcaster = newVideoBroadcaster()
	go session.broadcastVideo()

	return session, nil
}

func (n *Node) EnsureStream(serial string, opts streamcfg.Options) (*StreamSession, error) {
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

		return entry.Session, nil
	}

	entry = &streamEntry{
		Done: make(chan struct{}),
	}
	n.streams[serial] = entry
	n.streamsMu.Unlock()

	streamSession, err := n.StartStream(serial, opts)

	n.streamsMu.Lock()
	if err != nil {
		entry.Error = err
		delete(n.streams, serial)
	} else if streamSession == nil {
		err = errors.New("internal error: StartStream returned nil session")
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

func (n *Node) GetStream(serial string) (*StreamSession, error) {
	n.streamsMu.RLock()
	entry, ok := n.streams[serial]
	n.streamsMu.RUnlock()
	if !ok {
		return nil, errors.New("stream not found: " + serial)
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

func (n *Node) StopStream(serial string) error {
	n.streamsMu.Lock()
	entry, ok := n.streams[serial]

	if ok {
		delete(n.streams, serial)
	} else {
		n.streamsMu.Unlock()
		return errors.New("stream not found: " + serial)
	}

	n.streamsMu.Unlock()

	<-entry.Done

	if entry.Error != nil {
		return entry.Error
	}

	return entry.Session.Stop()
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
