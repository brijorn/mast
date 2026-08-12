//go:build darwin

package program

import "golang.org/x/sys/unix"

// procStateZombie is SZOMB from the BSD process states — a process that has
// exited but not yet been reaped.
const procStateZombie = 5

// processStartTime reads a PID's start time from the kernel via sysctl, the
// darwin equivalent of Linux's /proc/<pid>/stat. It is the run's proof that a
// live PID is still the process it started, since a reused PID belongs to a
// process that started later and so carries a different start time.
func processStartTime(pid int) (uint64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return 0, false
	}
	tv := kp.Proc.P_starttime
	started := uint64(tv.Sec)*1_000_000 + uint64(int64(tv.Usec))
	if started == 0 {
		return 0, false
	}
	return started, true
}

func procIsZombie(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return false
	}
	return kp.Proc.P_stat == procStateZombie
}

// processCwdMatchesRun has no cheap darwin equivalent (no /proc), and is only the
// fallback for a run recorded before start times were kept — which cannot exist
// on a node that has always run this build. Start-time identity is enough.
func processCwdMatchesRun(_ *Run) bool {
	return false
}
