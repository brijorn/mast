package program

import (
	"errors"
	"sync"
	"testing"

	"github.com/brijorn/mast/internal/node"
	"github.com/google/go-cmp/cmp"
)

type readinessObservation struct {
	device node.DeviceInfo
	ready  bool
}

type readinessObservingDevices struct {
	mu           sync.Mutex
	devices      []node.DeviceInfo
	observations []readinessObservation
}

func (d *readinessObservingDevices) ListDevices() ([]node.DeviceInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]node.DeviceInfo(nil), d.devices...), nil
}

func (d *readinessObservingDevices) DeviceBySerial(serial string) (*node.DeviceInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, device := range d.devices {
		if device.Serial == serial {
			copy := device
			return &copy, nil
		}
	}
	return nil, errors.New("device not found: " + serial)
}

func (d *readinessObservingDevices) ListNodes() []node.NodeInfo {
	return nil
}

func (d *readinessObservingDevices) ObserveDeviceReady(device node.DeviceInfo, ready bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observations = append(d.observations, readinessObservation{device: device, ready: ready})
}

func (d *readinessObservingDevices) setDevices(devices []node.DeviceInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.devices = append([]node.DeviceInfo(nil), devices...)
}

func (d *readinessObservingDevices) observationSnapshot() []readinessObservation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]readinessObservation(nil), d.observations...)
}

func TestAutostartReadinessObservationIncludesDisconnectWithoutRun(t *testing.T) {
	device := node.DeviceInfo{
		Serial:   "phone-1",
		Platform: node.PlatformAndroid,
		State:    "device",
		NodeID:   "node-1",
	}
	devices := &readinessObservingDevices{devices: []node.DeviceInfo{device}}
	store := &Store{
		devices:             devices,
		runs:                make(map[string]*runState),
		observedDeviceReady: make(map[string]bool),
		observedDevices:     make(map[string]node.DeviceInfo),
	}

	store.checkAutostartReconnects()
	devices.setDevices(nil)
	store.checkAutostartReconnects()

	want := []readinessObservation{
		{device: device, ready: true},
		{device: device, ready: false},
	}
	if diff := cmp.Diff(want, devices.observationSnapshot(), cmp.AllowUnexported(readinessObservation{})); diff != "" {
		t.Fatalf("readiness observations mismatch (-want +got):\n%s", diff)
	}
}
