package program

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// A run's processes are placed in a transient systemd slice of their own.
//
// A process group is a courtesy: anything that calls setsid — a daemon, a
// service a runner starts behind the program's back — leaves it, and the kill
// that ends the run then misses it. Wine is the case that forced this. Its
// server is started on demand by the first .exe, survives the launcher, parents
// every later game, and holds the log pipes the first run handed it, so a stop
// killed the launcher, left the game playing, and left Mast waiting on a pipe
// that would never close.
//
// A cgroup cannot be escaped that way. Everything a run forks stays in the
// slice whatever it does with sessions and process groups, so one kill on the
// slice ends the run and everything it started, and the pipes close with it.
//
// Each process gets its own scope inside the shared slice because a scope
// wraps exactly one command; the slice is the unit of stopping, and the run
// name it carries is what makes the kill precise instead of fleet-wide.

const runSlicePrefix = "mast-run-"

// runSliceName is the transient slice holding one run's processes.
//
// systemd reads dashes in a unit name as path separators, so a run's own UUID
// would ask for a tree of empty parent slices. Stripped of them, every run
// lands beside its siblings under mast-run.slice.
func runSliceName(runID string) string {
	var name strings.Builder
	for _, r := range strings.ToLower(runID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			name.WriteRune(r)
		}
	}
	if name.Len() == 0 {
		return ""
	}
	return runSlicePrefix + name.String() + ".slice"
}

// scopedCommand rewrites a run's command to start inside the run's slice.
//
// It returns the command untouched wherever transient units are not something
// this host can make — a macOS peer, a Termux node, a Linux box with no user
// manager. The kill path degrades with it: a run started outside a slice is
// still ended by its process group, exactly as before.
func scopedCommand(runID, command string, args []string) (string, []string) {
	slice := runSliceName(runID)
	systemdRun := systemdRunPath()
	if slice == "" || systemdRun == "" || !commandResolves(command) {
		return command, args
	}
	// No --unit: the slice is what gets killed, and letting systemd name each
	// scope means a resumed run can never collide with a scope its previous
	// attempt has not finished releasing.
	scoped := []string{"--user", "--scope", "--collect", "--quiet", "--slice=" + slice, "--", command}
	return systemdRun, append(scoped, args...)
}

// killRunSlice ends every process in the run's slice, whatever session or
// process group it moved itself into.
//
// It is best effort and deliberately not the only kill: a run started before
// this existed, or on a host that cannot make transient units, has no slice to
// name, and the process-group kill that follows is what ends it. Reporting
// failure here would turn "this run has no slice" into a stop that looks
// broken.
func killRunSlice(runID string) {
	slice := runSliceName(runID)
	if slice == "" || systemdRunPath() == "" {
		return
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return
	}
	_ = exec.Command(systemctl, "--user", "kill", "--signal=SIGKILL", slice).Run()
}

// commandResolves reports whether starting this command is worth wrapping.
//
// A command that does not exist has to keep failing where it always failed —
// at the start, with the name of the thing that is missing. Wrapped, it would
// start perfectly well and die inside the scope instead, turning a companion
// Mast could report as unstartable into one that merely exits.
func commandResolves(command string) bool {
	if strings.ContainsRune(command, os.PathSeparator) {
		info, err := os.Stat(command)
		return err == nil && !info.IsDir()
	}
	_, err := exec.LookPath(command)
	return err == nil
}

var systemdRunOnce struct {
	sync.Once
	path string
}

// systemdRunPath is systemd-run, or empty where it cannot place a scope.
//
// Finding the binary is not enough: a transient unit is registered over the
// user bus, so a host without a running user manager would fail at exec, after
// the run had already been recorded as started.
func systemdRunPath() string {
	systemdRunOnce.Do(func() {
		if runtime.GOOS != "linux" {
			return
		}
		if os.Getenv("XDG_RUNTIME_DIR") == "" || os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
			return
		}
		path, err := exec.LookPath("systemd-run")
		if err != nil {
			return
		}
		systemdRunOnce.path = path
	})
	return systemdRunOnce.path
}
