//go:build windows

package node

import (
	"fmt"
	"os/exec"
	"strings"
)

// findADBServerProcesses returns the adb processes listening on port. A node can
// have something else on that port too — finn forwards its tailnet address to
// the loopback server with a netsh portproxy, which listens through svchost — so
// only listeners on a loopback or wildcard address that are adb.exe count.
func findADBServerProcesses(port int) ([]int, error) {
	output, err := exec.Command("netstat", "-ano").Output()
	if err != nil {
		return nil, fmt.Errorf("netstat: %w", err)
	}
	var pids []int
	for _, pid := range parseNetstatListeners(string(output), port) {
		image, err := processImageName(pid)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(image, "adb.exe") {
			pids = append(pids, pid)
		}
	}
	if len(pids) == 0 {
		return nil, errADBServerNotFound
	}
	return pids, nil
}

func processImageName(pid int) (string, error) {
	output, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return "", fmt.Errorf("tasklist for pid %d: %w", pid, err)
	}
	return parseTasklistImageName(string(output)), nil
}
