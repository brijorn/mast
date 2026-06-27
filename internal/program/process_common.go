package program

import (
	"path/filepath"
	"strings"
	"time"
)

func waitForRunProcessExit(run *Run, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, _ := runProcessStatus(run)
		if !alive {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	alive, _ := runProcessStatus(run)
	return !alive
}

func commandLineMatches(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] == want[i] {
			continue
		}
		if i == 0 && sameCommandName(got[i], want[i]) {
			continue
		}
		return false
	}
	return true
}

func commandLineStringMatches(got string, want []string) bool {
	got = strings.TrimSpace(got)
	if got == "" {
		return false
	}
	for _, part := range want {
		if part == "" {
			continue
		}
		if strings.Contains(got, part) {
			continue
		}
		if sameCommandName(filepath.Base(got), part) {
			continue
		}
		return false
	}
	return true
}

func sameCommandName(a, b string) bool {
	a = strings.TrimSuffix(a, " (deleted)")
	return a != "" && b != "" && (a == b || filepath.Base(a) == filepath.Base(b))
}
