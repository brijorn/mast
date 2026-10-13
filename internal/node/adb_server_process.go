package node

import (
	"encoding/csv"
	"net"
	"strconv"
	"strings"
)

// parseNetstatListeners reads `netstat -ano` and returns the PIDs listening on
// a loopback or wildcard address at port. A listener is recognised by its
// remote side being the unspecified address with port 0, not by the state
// column, which Windows localises.
func parseNetstatListeners(output string, port int) []int {
	var pids []int
	seen := make(map[int]struct{})
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		localHost, localPort, err := net.SplitHostPort(fields[1])
		if err != nil || localPort != strconv.Itoa(port) {
			continue
		}
		if ip := net.ParseIP(localHost); ip == nil || !(ip.IsLoopback() || ip.IsUnspecified()) {
			continue
		}
		remoteHost, remotePort, err := net.SplitHostPort(fields[2])
		if err != nil || remotePort != "0" {
			continue
		}
		if ip := net.ParseIP(remoteHost); ip == nil || !ip.IsUnspecified() {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 {
			continue
		}
		if _, duplicate := seen[pid]; duplicate {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	return pids
}

// parseTasklistImageName reads the image name from one row of
// `tasklist /FO CSV /NH`. A PID with no process prints an INFO line, which has
// no quoted fields and yields "".
func parseTasklistImageName(output string) string {
	records, err := csv.NewReader(strings.NewReader(strings.TrimSpace(output))).ReadAll()
	if err != nil || len(records) == 0 || len(records[0]) < 2 {
		return ""
	}
	return records[0][0]
}
