//go:build !windows

package node

import (
	"context"
	"fmt"
	"runtime"
)

// Only Windows nodes have hung their adb server. Elsewhere the daemon's place
// matters more than its health: on Linux a server restarted from Mast would
// live in Mast's own service cgroup, so the next restart of Mast would take
// every phone's transport down with it. The watchdog still reports the hang and
// holds the power policy off; replacing the server is left to an operator.
func findADBServerProcesses(int) ([]int, error) {
	return nil, fmt.Errorf("replacing a hung adb server is not supported on %s", runtime.GOOS)
}

// Restarting one phone's USB device is Windows-only; elsewhere the watchdog
// does not chase phones that drop out of adb.
var restartUSBDevice func(ctx context.Context, serial string) error
