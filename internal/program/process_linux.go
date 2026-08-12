//go:build linux

package program

import (
	"os"
	"strconv"
	"strings"
)

// processStartTime reads a PID's start time from /proc.
//
// The command name sits in parentheses in the middle of the line and may hold
// both spaces and parentheses of its own, so the fields are counted from the
// last ')' rather than from the beginning. Everything after it is positional:
// state is field 3, and start time is field 22.
func processStartTime(pid int) (uint64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data)[end+1:])
	const startTimeField = 22 - 3
	if len(fields) <= startTimeField {
		return 0, false
	}
	started, err := strconv.ParseUint(fields[startTimeField], 10, 64)
	if err != nil {
		return 0, false
	}
	return started, true
}

func procIsZombie(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] == "Z"
}

func processCwdMatchesRun(run *Run) bool {
	if run.PID <= 0 || run.Workspace == "" {
		return false
	}
	cwd, err := os.Readlink("/proc/" + strconv.Itoa(run.PID) + "/cwd")
	if err != nil {
		return false
	}
	return cwd == run.Workspace
}
