package node

import (
	"context"
	"testing"
	"time"

	"github.com/brijorn/mast/internal/update"
)

func TestCheckNodeUpdateRoutesToPeer(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	nodeB.updateChecker = &fakeUpdateChecker{
		check: &update.CheckResult{
			CurrentVersion:  "0.1.0",
			LatestVersion:   "0.2.0",
			UpdateAvailable: true,
			OS:              "darwin",
			Arch:            "arm64",
		},
	}
	connectNodePair(t, nodeA, nodeB)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := nodeA.CheckNodeUpdate(ctx, "b")
	if err != nil {
		t.Fatalf("CheckNodeUpdate returned error: %v", err)
	}

	if got.CurrentVersion != "0.1.0" || got.LatestVersion != "0.2.0" || !got.UpdateAvailable {
		t.Fatalf("result = %+v", got)
	}
}

func TestApplyNodeUpdateRoutesToPeer(t *testing.T) {
	nodeA, nodeB := createNodePair(t)
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()
	restartScheduled := make(chan struct{}, 1)
	nodeB.scheduleRestart = func(time.Duration) error {
		select {
		case restartScheduled <- struct{}{}:
		default:
		}
		return nil
	}

	nodeB.updateApplier = &fakeUpdateApplier{
		result: &update.ApplyResult{
			CurrentVersion:  "0.1.0",
			LatestVersion:   "0.2.0",
			Updated:         true,
			RestartRequired: true,
			Message:         "updated",
		},
	}
	connectNodePair(t, nodeA, nodeB)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := nodeA.ApplyNodeUpdate(ctx, "b", update.ApplyOptions{Force: true, Restart: true})
	if err != nil {
		t.Fatalf("ApplyNodeUpdate returned error: %v", err)
	}

	if !got.Updated || !got.RestartRequired || got.Message != "updated" {
		t.Fatalf("result = %+v", got)
	}
	if !nodeB.updateApplier.(*fakeUpdateApplier).force {
		t.Fatal("Force was not forwarded to peer applier")
	}
	if !nodeB.updateApplier.(*fakeUpdateApplier).restart {
		t.Fatal("Restart was not forwarded to peer applier")
	}
	select {
	case <-restartScheduled:
	case <-time.After(time.Second):
		t.Fatal("restart was not scheduled")
	}
}

type fakeUpdateChecker struct {
	check *update.CheckResult
	err   error
}

func (f *fakeUpdateChecker) Check(_ context.Context) (*update.CheckResult, error) {
	return f.check, f.err
}

type fakeUpdateApplier struct {
	result  *update.ApplyResult
	err     error
	force   bool
	restart bool
}

func (f *fakeUpdateApplier) Apply(_ context.Context, opts update.ApplyOptions) (*update.ApplyResult, error) {
	f.force = opts.Force
	f.restart = opts.Restart
	return f.result, f.err
}
