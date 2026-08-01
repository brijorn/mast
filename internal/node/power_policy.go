package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/brijorn/mast/internal/scrcpy"
)

const (
	devicePowerReassertInterval = 30 * time.Second
	devicePowerRetryInitial     = 5 * time.Second
	devicePowerRetryMaximum     = time.Minute
	devicePowerPolicySCID       = 0x4d415354 // "MAST", kept separate from viewer scrcpy sessions.
)

type devicePowerScrcpyEndpoint struct {
	scidArgument string
	deviceSocket string
}

func devicePowerPolicyEndpoint() devicePowerScrcpyEndpoint {
	scid := fmt.Sprintf("%08x", devicePowerPolicySCID)
	return devicePowerScrcpyEndpoint{
		scidArgument: "scid=" + scid,
		deviceSocket: "localabstract:scrcpy_" + scid,
	}
}

type devicePowerAttempt struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	session *devicePowerSession
}

func newDevicePowerAttempt(parent context.Context) *devicePowerAttempt {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &devicePowerAttempt{ctx: ctx, cancel: cancel}
}

func (a *devicePowerAttempt) attach(session *devicePowerSession) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ctx.Err(); err != nil {
		return err
	}
	a.session = session
	return nil
}

func (a *devicePowerAttempt) release() {
	a.mu.Lock()
	a.session = nil
	a.mu.Unlock()
	a.cancel()
}

func (a *devicePowerAttempt) stop() error {
	a.cancel()
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.stop()
}

type devicePowerRetry struct {
	cancel context.CancelFunc
}

// devicePowerSession deliberately keeps a control-only scrcpy server alive per
// ready Android device. SET_DISPLAY_POWER is the only implementation available
// across Mast's supported Android versions; Android's direct display-power
// shell command is only available on newer releases, and a viewer-bound timer
// cannot protect a device with no viewers. The extra process/socket is the
// tradeoff, but video and audio encoders stay disabled. A dedicated scid avoids
// viewer reverse-socket churn, periodic writes correct later physical wakes,
// and cleanup=false keeps scrcpy from restoring display power. We intentionally
// do not use power_off_on_close: scrcpy implements it with KEYCODE_POWER, which
// can put the automation device itself to sleep instead of only darkening the
// physical panel.
type devicePowerSession struct {
	serial   string
	listener net.Listener
	control  net.Conn
	cmd      *exec.Cmd

	controlMu sync.Mutex
	stopOnce  sync.Once
	stopErr   error
}

func (s *devicePowerSession) setDisplayPower(on bool) error {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if s.control == nil {
		return errors.New("device power control connection not available")
	}
	return scrcpy.WriteSetDisplayPower(s.control, on)
}

func (s *devicePowerSession) stop() error {
	s.stopOnce.Do(func() {
		if s.listener != nil {
			s.stopErr = s.listener.Close()
		}

		s.controlMu.Lock()
		if s.control != nil {
			if err := s.control.Close(); err != nil && s.stopErr == nil {
				s.stopErr = err
			}
		}
		s.controlMu.Unlock()

		if s.cmd != nil && s.cmd.Process != nil {
			killErr := s.cmd.Process.Kill()
			if killErr != nil && s.stopErr == nil {
				s.stopErr = killErr
			}
			if waitErr := s.cmd.Wait(); waitErr != nil && s.stopErr == nil && killErr != nil {
				s.stopErr = waitErr
			}
		}
	})
	return s.stopErr
}

// ObserveDeviceReady receives the readiness observation already maintained by
// the program autostart monitor. Power policy remains node-owned: the program
// store only reports the transition, and the owning node ignores peer and iOS
// devices.
func (n *Node) ObserveDeviceReady(device DeviceInfo, ready bool) {
	if device.Serial == "" || device.NodeID != n.ID || device.Platform != PlatformAndroid {
		return
	}

	n.devicePowerMu.Lock()
	var attempt *devicePowerAttempt
	var retry *devicePowerRetry
	if ready {
		n.devicePowerReady[device.Serial] = true
	} else {
		delete(n.devicePowerReady, device.Serial)
		attempt = n.devicePowerStarting[device.Serial]
		delete(n.devicePowerStarting, device.Serial)
		retry = n.devicePowerRetries[device.Serial]
		delete(n.devicePowerRetries, device.Serial)
		delete(n.devicePowerFailures, device.Serial)
	}
	session := n.devicePowerSessions[device.Serial]
	if !ready {
		delete(n.devicePowerSessions, device.Serial)
	}
	n.devicePowerMu.Unlock()

	if !ready {
		if retry != nil {
			retry.cancel()
		}
		if attempt != nil {
			if err := attempt.stop(); err != nil {
				log.Printf("stop starting device power policy for %s after disconnect: %v", device.Serial, err)
			}
		}
		if session != nil {
			if err := session.stop(); err != nil {
				log.Printf("stop device power policy for %s after disconnect: %v", device.Serial, err)
			}
		}
	}
	n.requestDevicePowerPolicy()
}

func (n *Node) requestDevicePowerPolicy() {
	if n.devicePowerWake == nil {
		return
	}
	select {
	case n.devicePowerWake <- struct{}{}:
	default:
	}
}

func (n *Node) monitorDevicePowerPolicy() {
	ticker := time.NewTicker(devicePowerReassertInterval)
	defer ticker.Stop()
	defer n.stopAllDevicePowerSessions()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-n.devicePowerWake:
			n.reconcileDevicePowerPolicy()
		case <-ticker.C:
			n.reconcileDevicePowerPolicy()
		}
	}
}

func (n *Node) devicePowerConfig() (managed bool, keepDisplayOff bool) {
	n.configMu.RLock()
	defer n.configMu.RUnlock()
	if !n.configReady || !n.configState.AndroidEnabled {
		return false, false
	}
	return true, n.configState.KeepDisplayOff
}

func (n *Node) reconcileDevicePowerPolicy() {
	managed, keepDisplayOff := n.devicePowerConfig()
	if !managed {
		n.stopAllDevicePowerSessions()
		return
	}
	if !keepDisplayOff {
		// Disarm active, pending, and retrying display-off work before applying
		// the independent stay-awake policy below.
		n.stopAllDevicePowerSessions()
	}

	n.devicePowerMu.Lock()
	serials := make([]string, 0, len(n.devicePowerReady))
	for serial := range n.devicePowerReady {
		serials = append(serials, serial)
	}
	n.devicePowerMu.Unlock()

	for _, serial := range serials {
		go n.reconcileDevicePower(serial)
	}
}

func (n *Node) reconcileDevicePower(serial string) {
	if err := n.assertDeviceStayAwake(serial); err != nil {
		log.Printf("apply stay-awake policy to %s: %v", serial, err)
	}
	managed, keepDisplayOff := n.devicePowerConfig()
	if !managed || !keepDisplayOff {
		return
	}

	n.devicePowerMu.Lock()
	if !n.devicePowerReady[serial] {
		n.devicePowerMu.Unlock()
		return
	}
	if session := n.devicePowerSessions[serial]; session != nil {
		n.devicePowerMu.Unlock()
		if err := session.setDisplayPower(false); err != nil {
			log.Printf("re-assert display-off policy for %s: %v", serial, err)
			n.discardDevicePowerSession(serial, session)
		}
		return
	}
	if n.devicePowerStarting[serial] != nil || n.devicePowerRetries[serial] != nil {
		n.devicePowerMu.Unlock()
		return
	}
	attempt := newDevicePowerAttempt(n.ctx)
	if n.devicePowerStarting == nil {
		n.devicePowerStarting = make(map[string]*devicePowerAttempt)
	}
	n.devicePowerStarting[serial] = attempt
	n.devicePowerMu.Unlock()

	session, err := n.startDevicePowerSession(attempt, serial)
	attemptCanceled := attempt.ctx.Err() != nil
	managed, policyEnabled := n.devicePowerConfig()

	n.devicePowerMu.Lock()
	if n.devicePowerStarting[serial] == attempt {
		delete(n.devicePowerStarting, serial)
	}
	keepSession := err == nil && managed && policyEnabled && n.devicePowerReady[serial] && n.devicePowerSessions[serial] == nil
	if keepSession {
		n.devicePowerSessions[serial] = session
		delete(n.devicePowerFailures, serial)
	}
	n.devicePowerMu.Unlock()
	attempt.release()

	if err != nil {
		if !attemptCanceled && managed && policyEnabled {
			log.Printf("start display-off policy for %s: %v", serial, err)
			n.scheduleDevicePowerPolicyRetry(serial)
		}
		return
	}
	if !keepSession {
		_ = session.stop()
		return
	}
	if currentManaged, currentEnabled := n.devicePowerConfig(); !currentManaged || !currentEnabled {
		n.discardDevicePowerSession(serial, session)
		return
	}

	go n.watchDevicePowerSession(serial, session)
	if err := session.setDisplayPower(false); err != nil {
		log.Printf("apply display-off policy to %s: %v", serial, err)
		n.discardDevicePowerSession(serial, session)
	}
}

func (n *Node) assertDeviceStayAwake(serial string) error {
	_, err := n.adbShell(n.ctx, "", serial, "settings", "put", "global", "stay_on_while_plugged_in", "7")
	return err
}

func (n *Node) startDevicePowerSession(attempt *devicePowerAttempt, serial string) (*devicePowerSession, error) {
	if err := n.pushScrcpyServerContext(attempt.ctx, "", serial); err != nil {
		return nil, err
	}

	listener, port, err := newScrcpyListener()
	if err != nil {
		return nil, err
	}
	session := &devicePowerSession{serial: serial, listener: listener}
	if err := attempt.attach(session); err != nil {
		_ = session.stop()
		return nil, err
	}

	endpoint := devicePowerPolicyEndpoint()
	if err := n.adbReverse(attempt.ctx, "", serial, endpoint.deviceSocket, port); err != nil {
		_ = session.stop()
		return nil, err
	}

	cmd, err := n.adbStartShell("", serial, devicePowerScrcpyArgs(endpoint)...)
	if err != nil {
		_ = session.stop()
		return nil, err
	}
	session.cmd = cmd

	control, err := acceptScrcpySocket(listener)
	if err != nil {
		diagnostics := ""
		if cmd != nil && cmd.Stderr != nil {
			if stderr, ok := cmd.Stderr.(interface{ String() string }); ok && stderr.String() != "" {
				diagnostics = "\nscrcpy stderr:\n" + stderr.String()
			}
		}
		_ = session.stop()
		return nil, fmt.Errorf("accept device power control socket: %w%s", err, diagnostics)
	}
	session.control = control
	return session, nil
}

func devicePowerScrcpyArgs(endpoint devicePowerScrcpyEndpoint) []string {
	return []string{
		"CLASSPATH=" + scrcpy.RemotePath,
		"app_process",
		"/",
		"com.genymobile.scrcpy.Server",
		scrcpy.ServerVersion,
		endpoint.scidArgument,
		"video=false",
		"audio=false",
		"control=true",
		"stay_awake=false",
		"power_on=false",
		"cleanup=false",
		"clipboard_autosync=false",
		"send_device_meta=false",
	}
}

func (n *Node) watchDevicePowerSession(serial string, session *devicePowerSession) {
	_, _ = io.Copy(io.Discard, session.control)
	n.discardDevicePowerSession(serial, session)
}

func (n *Node) discardDevicePowerSession(serial string, session *devicePowerSession) {
	n.devicePowerMu.Lock()
	removed := false
	if n.devicePowerSessions[serial] == session {
		delete(n.devicePowerSessions, serial)
		removed = true
	}
	n.devicePowerMu.Unlock()
	if err := session.stop(); err != nil {
		log.Printf("stop device power policy for %s: %v", serial, err)
	}
	if removed {
		n.scheduleDevicePowerPolicyRetry(serial)
	}
}

func devicePowerRetryDelay(failures uint) time.Duration {
	delay := devicePowerRetryInitial
	for failure := uint(1); failure < failures && delay < devicePowerRetryMaximum; failure++ {
		if delay > devicePowerRetryMaximum/2 {
			return devicePowerRetryMaximum
		}
		delay *= 2
	}
	if delay > devicePowerRetryMaximum {
		return devicePowerRetryMaximum
	}
	return delay
}

func (n *Node) scheduleDevicePowerPolicyRetry(serial string) {
	managed, keepDisplayOff := n.devicePowerConfig()
	if !managed || !keepDisplayOff {
		return
	}

	n.devicePowerMu.Lock()
	if !n.devicePowerReady[serial] || n.devicePowerSessions[serial] != nil ||
		n.devicePowerStarting[serial] != nil || n.devicePowerRetries[serial] != nil {
		n.devicePowerMu.Unlock()
		return
	}
	if n.devicePowerRetries == nil {
		n.devicePowerRetries = make(map[string]*devicePowerRetry)
	}
	if n.devicePowerFailures == nil {
		n.devicePowerFailures = make(map[string]uint)
	}
	failures := n.devicePowerFailures[serial] + 1
	n.devicePowerFailures[serial] = failures
	parent := n.ctx
	if parent == nil {
		parent = context.Background()
	}
	retryCtx, cancel := context.WithCancel(parent)
	retry := &devicePowerRetry{cancel: cancel}
	n.devicePowerRetries[serial] = retry
	n.devicePowerMu.Unlock()

	go func() {
		timer := time.NewTimer(devicePowerRetryDelay(failures))
		defer timer.Stop()
		select {
		case <-retryCtx.Done():
		case <-timer.C:
			n.devicePowerMu.Lock()
			if n.devicePowerRetries[serial] != retry {
				n.devicePowerMu.Unlock()
				return
			}
			delete(n.devicePowerRetries, serial)
			ready := n.devicePowerReady[serial]
			n.devicePowerMu.Unlock()
			managed, keepDisplayOff := n.devicePowerConfig()
			if !ready || !managed || !keepDisplayOff {
				return
			}
			n.requestDevicePowerPolicy()
		}
	}()
}

func (n *Node) reassertDevicePowerPolicy(serial string) {
	managed, keepDisplayOff := n.devicePowerConfig()
	if !managed {
		return
	}
	n.devicePowerMu.Lock()
	ready := n.devicePowerReady[serial]
	session := n.devicePowerSessions[serial]
	n.devicePowerMu.Unlock()
	if !ready {
		return
	}
	if err := n.assertDeviceStayAwake(serial); err != nil {
		log.Printf("re-assert stay-awake policy for %s: %v", serial, err)
	}
	if !keepDisplayOff {
		return
	}

	if session == nil {
		n.requestDevicePowerPolicy()
		return
	}
	if err := session.setDisplayPower(false); err != nil {
		log.Printf("re-assert display-off policy for %s after stream churn: %v", serial, err)
		n.discardDevicePowerSession(serial, session)
	}
}

func (n *Node) stopAllDevicePowerSessions() {
	n.devicePowerMu.Lock()
	sessions := make([]*devicePowerSession, 0, len(n.devicePowerSessions))
	for serial, session := range n.devicePowerSessions {
		sessions = append(sessions, session)
		delete(n.devicePowerSessions, serial)
	}
	attempts := make([]*devicePowerAttempt, 0, len(n.devicePowerStarting))
	for serial, attempt := range n.devicePowerStarting {
		attempts = append(attempts, attempt)
		delete(n.devicePowerStarting, serial)
	}
	retries := make([]*devicePowerRetry, 0, len(n.devicePowerRetries))
	for serial, retry := range n.devicePowerRetries {
		retries = append(retries, retry)
		delete(n.devicePowerRetries, serial)
	}
	clear(n.devicePowerFailures)
	n.devicePowerMu.Unlock()

	for _, retry := range retries {
		retry.cancel()
	}
	for _, attempt := range attempts {
		if err := attempt.stop(); err != nil {
			log.Printf("stop starting device power policy: %v", err)
		}
	}
	for _, session := range sessions {
		if err := session.stop(); err != nil {
			log.Printf("stop device power policy for %s: %v", session.serial, err)
		}
	}
}
