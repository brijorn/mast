//go:build !windows

package program

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// startProcessThatLeavesWorkspace starts a process in workspace that
// immediately moves to /, the way Wine steps into the wineserver's socket
// directory while it starts that server.
func startProcessThatLeavesWorkspace(t *testing.T, workspace string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "cd / && exec sleep 30")
	cmd.Dir = workspace
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cwd, err := os.Readlink("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/cwd")
		if err == nil && cwd != workspace {
			return cmd
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("process never left the workspace")
	return nil
}

func TestRunProcessStatusKeepsAProcessThatChangedDirectory(t *testing.T) {
	workspace := t.TempDir()
	cmd := startProcessThatLeavesWorkspace(t, workspace)

	run := &Run{Workspace: workspace}
	setRunPID(run, cmd.Process.Pid)
	if run.PIDStartTime == 0 {
		t.Fatal("expected a start time to be recorded alongside the PID")
	}

	alive, matches := runProcessStatus(run)
	if !alive {
		t.Fatal("expected the process to be alive")
	}
	if !matches {
		t.Fatal("a running process that moved out of its workspace was mistaken for a stranger")
	}
}

// A run recorded before start times were kept still falls back to the working
// directory, which is why it has to be replaced rather than merely trusted.
func TestRunProcessStatusFallsBackToWorkingDirectoryWithoutAStartTime(t *testing.T) {
	workspace := t.TempDir()
	cmd := startProcessThatLeavesWorkspace(t, workspace)

	run := &Run{Workspace: workspace, PID: cmd.Process.Pid}
	alive, matches := runProcessStatus(run)
	if !alive {
		t.Fatal("expected the process to be alive")
	}
	if matches {
		t.Fatal("expected the legacy working-directory test to be the one applied")
	}
}

func TestClearRunPIDForgetsTheStartTime(t *testing.T) {
	run := &Run{}
	setRunPID(run, os.Getpid())
	if run.PID == 0 || run.PIDStartTime == 0 {
		t.Fatal("expected the PID and its start time to be recorded")
	}
	clearRunPID(run)
	if run.PID != 0 || run.PIDStartTime != 0 {
		t.Fatalf("expected both to be cleared, got pid=%d start=%d", run.PID, run.PIDStartTime)
	}
}

// A PID that is reused by an unrelated process must not be adopted.
func TestRunProcessStatusRejectsAReusedPID(t *testing.T) {
	workspace := t.TempDir()
	cmd := exec.Command("sh", "-c", "exec sleep 30")
	cmd.Dir = workspace
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	run := &Run{Workspace: workspace}
	setRunPID(run, cmd.Process.Pid)
	// The same PID, claimed by a run that started something else earlier.
	run.PIDStartTime--

	alive, matches := runProcessStatus(run)
	if !alive {
		t.Fatal("expected the process to be alive")
	}
	if matches {
		t.Fatal("expected a PID whose start time disagrees to be refused")
	}
}
