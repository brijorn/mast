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

// parsePnputilUSBInstanceID finds a connected USB device by the serial its
// instance ID ends in, in the output of `pnputil /enum-devices /connected`.
// Android phones report their adb serial as the USB serial number, so the
// parent device of the transport adb names R5GL5029RMF is
// USB\VID_04E8&PID_6860\R5GL5029RMF.
func parsePnputilUSBInstanceID(output string, serial string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		index := strings.Index(strings.ToUpper(line), `USB\VID_`)
		if index == -1 {
			continue
		}
		id := strings.TrimSpace(line[index:])
		if fields := strings.Fields(id); len(fields) > 0 {
			id = fields[0]
		}
		lastSlash := strings.LastIndex(id, `\`)
		if lastSlash == -1 || strings.Count(id, `\`) != 2 {
			continue
		}
		if strings.EqualFold(id[lastSlash+1:], serial) {
			return id, true
		}
	}
	return "", false
}
