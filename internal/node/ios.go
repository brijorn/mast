package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brijorn/ioslink"
	streamcfg "github.com/brijorn/mast/internal/stream"
	"github.com/danielpaulus/go-ios/ios/tunnel"
	"github.com/google/uuid"
)

const (
	iosCommandTimeout = 10 * time.Second
	iosPresetTimeout  = 5 * time.Second
)

func (n *Node) listLocalIOSDevices() ([]DeviceInfo, error) {
	summaries, err := ioslink.ListDevices()
	if err != nil {
		return nil, err
	}
	devices := make([]DeviceInfo, 0, len(summaries))
	for _, summary := range summaries {
		if summary.UDID == "" {
			continue
		}
		devices = append(devices, DeviceInfo{
			Serial:   summary.UDID,
			Platform: PlatformIOS,
			State:    summary.State,
			NodeID:   n.ID,
		})
	}
	return devices, nil
}

func (n *Node) startLocalIOSStream(serial string, _ streamcfg.Options) (*StreamSession, error) {
	return n.startLocalIOSStreamWithOptions(serial)
}

func (n *Node) startLocalIOSStreamWithOptions(serial string) (*StreamSession, error) {
	device, err := n.localDeviceBySerial(serial)
	if err != nil {
		return nil, err
	}
	if device.Platform != PlatformIOS {
		return nil, fmt.Errorf("device %s is not iOS", serial)
	}
	if device.State != "device" {
		return nil, fmt.Errorf("device %s is %s", serial, device.State)
	}

	ctx, cancel := context.WithTimeout(n.ctx, 60*time.Second)
	defer cancel()

	mgr, err := n.sharedIOSTunnelManager()
	if err != nil {
		return nil, err
	}
	iosDevice := ioslink.NewDevice(mgr, serial, "")
	if err := iosDevice.Start(ctx); err != nil {
		return nil, fmt.Errorf("device start: %w", err)
	}
	cleanup := func() {
		_ = iosDevice.Close()
	}

	sizeCtx, sizeCancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer sizeCancel()
	size, err := iosDevice.WindowSize(sizeCtx)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("get iOS window size: %w", err)
	}
	if err := n.setIOSMJPEGPreset(iosDevice); err != nil && !isContextDeadline(err) {
		log.Printf("set iOS MJPEG preset for %s: %v", serial, err)
	}

	streamHost, err := n.streamHostForNode(n.ID)
	if err != nil {
		cleanup()
		return nil, err
	}

	return &StreamSession{
		ID:           uuid.NewString(),
		DeviceSerial: serial,
		Platform:     PlatformIOS,
		Kind:         "mjpeg",
		Host:         streamHost,
		MJPEGURL:     "/api/streams/mjpeg?serial=" + serial,
		Width:        int(size.Width),
		Height:       int(size.Height),
		iosDevice:    iosDevice,
		iosCleanup:   cleanup,
	}, nil
}

func (n *Node) setIOSMJPEGPreset(device *ioslink.Device) error {
	ctx, cancel := context.WithTimeout(n.ctx, iosPresetTimeout)
	defer cancel()
	return device.SetMJPEGSettings(ctx, ioslink.DefaultWDAMJPEGFramerate, ioslink.MJPEGStreamSettings{
		ScalingFactor:     ioslink.DefaultWDAMJPEGScale,
		ScreenshotQuality: ioslink.DefaultWDAMJPEGQuality,
	})
}

func isContextDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded")
}

func (n *Node) iosPairRecordPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mast", "RemotePairing")
}

func (n *Node) sharedIOSTunnelManager() (*tunnel.TunnelManager, error) {
	n.iosMu.Lock()
	defer n.iosMu.Unlock()
	if n.iosTunnelMgr != nil {
		return n.iosTunnelMgr, nil
	}
	mgr, err := ioslink.NewTunnelManagerWithOptions(ioslink.TunnelManagerOptions{
		PairRecordPath: n.iosPairRecordPath(),
	})
	if err != nil {
		return nil, fmt.Errorf("tunnel manager: %w", err)
	}
	n.iosTunnelMgr = mgr
	return mgr, nil
}

func (s *StreamSession) StreamMJPEG(ctx context.Context, w http.ResponseWriter) error {
	if s.Platform != PlatformIOS || s.Kind != "mjpeg" || s.iosDevice == nil {
		return errors.New("active stream is not an iOS MJPEG stream")
	}

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)

	return s.iosDevice.StreamMJPEG(ctx, func(frame ioslink.Frame) error {
		if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame.Bytes)); err != nil {
			return err
		}
		if _, err := w.Write(frame.Bytes); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
}

func (n *Node) localIOSScreenshot(serial string) ([]byte, error) {
	session, err := n.GetStream(serial)
	if err == nil && session.iosDevice != nil {
		ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
		defer cancel()
		return session.iosDevice.Screenshot(ctx)
	}

	session, err = n.startLocalIOSStreamWithOptions(serial)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Stop() }()

	ctx, cancel := context.WithTimeout(n.ctx, iosCommandTimeout)
	defer cancel()
	return session.iosDevice.Screenshot(ctx)
}
