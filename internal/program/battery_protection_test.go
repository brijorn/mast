package program

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	mastconfig "github.com/brijorn/mast/internal/config"
	"github.com/brijorn/mast/internal/node"
)

type batteryControlDevices struct {
	devices       []node.DeviceInfo
	pressedSerial string
	pressedKey    uint32
	stoppedStream string
}

func (f *batteryControlDevices) ListDevices() ([]node.DeviceInfo, error) {
	return f.devices, nil
}

func (f *batteryControlDevices) ListNodes() []node.NodeInfo {
	return nil
}

func (f *batteryControlDevices) PressKey(serial string, keycode uint32, metaState uint32) error {
	f.pressedSerial = serial
	f.pressedKey = keycode
	return nil
}

func (f *batteryControlDevices) StopStream(serial string) error {
	f.stoppedStream = serial
	return nil
}

func TestEvaluateBatteryProtectionStopsRunImmediately(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	battery := 19
	devices := &batteryControlDevices{
		devices: []node.DeviceInfo{{
			Serial:         "phone-1",
			State:          "device",
			BatteryPercent: &battery,
			PowerHealth:    "plugged_draining",
		}},
	}
	store := &Store{
		runs: map[string]*runState{
			"run-1": {run: &Run{ID: "run-1", Serial: "phone-1", Workspace: workspace, Status: RunStatusRunning}},
		},
		devices:          devices,
		batteryProtected: make(map[string]bool),
		batteryProtection: mastconfig.BatteryProtection{
			Enabled:       true,
			MinPercent:    20,
			ResumePercent: 50,
			StopProgram:   true,
			StopStream:    true,
			SendHome:      true,
		},
	}
	store.evaluateBatteryProtection()
	got := findRun(t, store, "run-1")
	if got.Status != RunStatusStopped || got.StoppedReason != "battery_protection" {
		t.Fatalf("run = %+v, want stopped for battery protection", got)
	}
	if devices.pressedSerial != "phone-1" || devices.pressedKey != 3 {
		t.Fatalf("home press = serial %q key %d, want phone-1 key 3", devices.pressedSerial, devices.pressedKey)
	}
	if devices.stoppedStream != "phone-1" {
		t.Fatalf("stoppedStream = %q, want phone-1", devices.stoppedStream)
	}
}

func TestEvaluateBatteryProtectionRecoversRunAtResumePercent(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	battery := 51
	devices := &batteryControlDevices{
		devices: []node.DeviceInfo{{
			Serial:         "phone-1",
			State:          "device",
			BatteryPercent: &battery,
			PowerHealth:    "charging",
		}},
	}
	store := &Store{
		runs: map[string]*runState{
			"run-1": {run: &Run{ID: "run-1", Serial: "phone-1", Workspace: workspace, Status: RunStatusStopped, StoppedReason: "battery_protection", Cmd: "missing-command"}},
		},
		devices:              devices,
		startCmd:             exec.Command,
		batteryProtected:     map[string]bool{"phone-1": true},
		batteryStreamStopped: map[string]bool{"phone-1": true},
		batteryProtection: mastconfig.BatteryProtection{
			Enabled:       true,
			MinPercent:    20,
			ResumePercent: 50,
			StopProgram:   true,
			StopStream:    true,
			SendHome:      true,
		},
	}

	store.evaluateBatteryProtection()

	store.batteryMu.Lock()
	protected := store.batteryProtected["phone-1"]
	streamStopped := store.batteryStreamStopped["phone-1"]
	store.batteryMu.Unlock()
	if protected || streamStopped {
		t.Fatalf("protection state = protected %v streamStopped %v, want cleared", protected, streamStopped)
	}
}
