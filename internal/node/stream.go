package node

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"slices"

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
	listener     net.Listener
	cmd          *exec.Cmd
}

func (s *StreamSession) Stop() error {
	var err error

	if s.listener != nil {
		if closeErr := s.listener.Close(); closeErr != nil {
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

func (n *Node) StartStream(serial string, opts streamcfg.Options) (*StreamSession, error) {
	tempFile, err := os.CreateTemp("", scrcpy.Filename)
	if err != nil {
		return nil, err
	}
	defer func(name string) {
		_ = os.Remove(name)
	}(tempFile.Name())

	defer func() { _ = tempFile.Close() }()

	if _, err := tempFile.Write(scrcpy.Server); err != nil {
		return nil, err
	}
	if err := tempFile.Close(); err != nil {
		return nil, err
	}

	devices, err := n.ListDevices()
	if err != nil {
		return nil, err
	}

	index := slices.IndexFunc(devices, func(d DeviceInfo) bool {
		return d.Serial == serial
	})
	if index == -1 {
		return nil, errors.New("device not found: " + serial)
	}
	device := devices[index]

	streamHost, err := n.streamHostForNode(device.NodeID)
	if err != nil {
		return nil, err
	}

	host, err := n.adbHostForNode(device.NodeID)
	if err != nil {
		return nil, err
	}

	if err := n.adb.Push(host, tempFile.Name(), scrcpy.RemotePath); err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}

	port := ln.Addr().(*net.TCPAddr).Port

	if err := n.adb.Reverse(host, scrcpy.DeviceSocket, port); err != nil {
		_ = ln.Close()
		return nil, err
	}

	shellArgs := []string{
		"CLASSPATH=" + scrcpy.RemotePath,
		"app_process",
		"/",
		"com.genymobile.scrcpy.Server",
		scrcpy.ServerVersion,
	}
	shellArgs = append(shellArgs, opts.Format()...)

	cmd, err := n.adb.StartShell(host, shellArgs...)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	return &StreamSession{
		ID:           uuid.NewString(),
		DeviceSerial: serial,
		Host:         streamHost,
		LocalPort:    port,
		listener:     ln,
		cmd:          cmd,
	}, nil
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
	} else {
		entry.Session = streamSession
	}
	n.streamsMu.Unlock()
	close(entry.Done)

	return streamSession, err
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
