//go:build windows

package node

import (
	"os/exec"
	"strconv"
)

// os.Process.Kill only terminates the immediate Windows process. An adb client
// stuck behind the server can survive that cancellation and remain parented to
// Mast. taskkill /T gives a timed-out command the same process-tree cleanup
// used for Windows program runs.
func configureADBCommandCancellation(cmd *exec.Cmd) {
	fallback := cmd.Cancel
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			if err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err == nil {
				return nil
			}
		}
		if fallback != nil {
			return fallback()
		}
		return nil
	}
}
