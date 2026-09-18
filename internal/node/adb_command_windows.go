//go:build windows

package node

import (
	"os/exec"
	"time"
)

// Bound the time exec waits for a canceled adb client's pipes to close. Killing
// an adb process tree is unsafe here: the shared daemon may descend from the
// client that started it, and taking that tree down disconnects every phone.
func configureADBCommandCancellation(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}
