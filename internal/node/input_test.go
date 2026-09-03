package node

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func newInputTestNode(t *testing.T) *Node {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Node{ID: "test-node", ctx: ctx}
}

// A gesture only means anything in sequence, so one device's input must run in
// the order it arrived however slow an earlier step is.
func TestEnqueueInputPreservesOrderPerSerial(t *testing.T) {
	n := newInputTestNode(t)

	var mu sync.Mutex
	var ran []int
	done := make(chan struct{})

	for step := 0; step < 25; step++ {
		n.enqueueInput("serial-a", "touch", "", func() error {
			if step == 0 {
				time.Sleep(20 * time.Millisecond)
			}
			mu.Lock()
			ran = append(ran, step)
			if len(ran) == 25 {
				close(done)
			}
			mu.Unlock()
			return nil
		})
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("input never drained")
	}

	mu.Lock()
	defer mu.Unlock()
	for index, step := range ran {
		if index != step {
			t.Fatalf("input ran out of order at %d: %v", index, ran)
		}
	}
}

// The regression this whole worker exists for: a swipe on one phone used to
// hold the peer read loop, so every other device on the node waited it out.
func TestEnqueueInputDoesNotBlockOtherSerials(t *testing.T) {
	n := newInputTestNode(t)

	release := make(chan struct{})
	blocked := make(chan struct{})
	n.enqueueInput("serial-slow", "swipe", "", func() error {
		close(blocked)
		<-release
		return nil
	})
	defer close(release)

	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("slow input never started")
	}

	ran := make(chan struct{})
	n.enqueueInput("serial-other", "tap", "", func() error {
		close(ran)
		return nil
	})

	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("input for another serial waited on the slow device")
	}
}

// Enqueuing is what the peer read loop calls, so it must return whether or not
// the device is still working through what it was already given.
func TestEnqueueInputReturnsWhileDeviceIsBusy(t *testing.T) {
	n := newInputTestNode(t)

	release := make(chan struct{})
	defer close(release)
	n.enqueueInput("serial-a", "swipe", "", func() error {
		<-release
		return nil
	})

	returned := make(chan struct{})
	go func() {
		n.enqueueInput("serial-a", "tap", "", func() error { return nil })
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked behind in-flight input")
	}
}

// A failing step is logged and skipped; it must not strand the rest of the
// gesture behind it.
func TestEnqueueInputSurvivesAFailingStep(t *testing.T) {
	n := newInputTestNode(t)

	n.enqueueInput("serial-a", "touch", "", func() error { return errors.New("no control connection") })

	ran := make(chan struct{})
	n.enqueueInput("serial-a", "touch", "", func() error {
		close(ran)
		return nil
	})

	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("input after a failure never ran")
	}
}

// Workers stop with the node rather than outliving it.
func TestInputWorkerStopsWithNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{ID: "test-node", ctx: ctx}

	started := make(chan struct{})
	n.enqueueInput("serial-a", "tap", "", func() error {
		close(started)
		return nil
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker never ran")
	}

	cancel()

	queue := n.inputQueue("serial-a")
	deadline := time.Now().Add(2 * time.Second)
	for {
		queue.mu.Lock()
		closed := queue.closed
		queue.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never observed the node stopping")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A queue that has stopped accepts no further work rather than banking it.
	n.enqueueInput("serial-a", "tap", "", func() error { return nil })
	queue.mu.Lock()
	pending := len(queue.pending)
	queue.mu.Unlock()
	if pending != 0 {
		t.Fatalf("stopped queue accepted %d jobs", pending)
	}
}

// The regression behind "long pause, then it scrolls on its own": a device too
// slow to keep up banked every stale move and replayed them later. A move says
// only where the finger is now, so a newer one must supersede the pending one.
func TestEnqueueInputCoalescesPendingMoves(t *testing.T) {
	n := newInputTestNode(t)

	release := make(chan struct{})
	blocked := make(chan struct{})
	n.enqueueInput("serial-a", "touch", "", func() error {
		close(blocked)
		<-release
		return nil
	})
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("first job never started")
	}

	var mu sync.Mutex
	var ran []int
	for step := 0; step < 50; step++ {
		n.enqueueInput("serial-a", "touch", "move:0:true", func() error {
			mu.Lock()
			ran = append(ran, step)
			mu.Unlock()
			return nil
		})
	}

	queue := n.inputQueue("serial-a")
	queue.mu.Lock()
	pending := len(queue.pending)
	queue.mu.Unlock()
	if pending != 1 {
		t.Fatalf("50 pending moves collapsed to %d, want 1", pending)
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := append([]int(nil), ran...)
		mu.Unlock()
		if len(got) == 1 {
			// Only the newest move survives; the stale ones are not replayed.
			if got[0] != 49 {
				t.Fatalf("delivered stale move %d, want the newest (49)", got[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ran = %v, want just the newest move", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Coalescing must not reach past a non-move: a move either side of an UP is a
// different gesture, and collapsing across it would rewrite what happened.
func TestEnqueueInputDoesNotCoalesceAcrossOtherInput(t *testing.T) {
	n := newInputTestNode(t)

	release := make(chan struct{})
	defer close(release)
	blocked := make(chan struct{})
	n.enqueueInput("serial-a", "touch", "", func() error {
		close(blocked)
		<-release
		return nil
	})
	<-blocked

	n.enqueueInput("serial-a", "touch", "move:0:true", func() error { return nil })
	n.enqueueInput("serial-a", "touch", "", func() error { return nil })
	n.enqueueInput("serial-a", "touch", "move:0:true", func() error { return nil })

	queue := n.inputQueue("serial-a")
	queue.mu.Lock()
	pending := len(queue.pending)
	queue.mu.Unlock()
	if pending != 3 {
		t.Fatalf("pending = %d, want 3 (no coalescing across the UP)", pending)
	}
}

// Two pointers are two fingers; their moves must not collapse into each other.
func TestEnqueueInputCoalescesPerPointer(t *testing.T) {
	n := newInputTestNode(t)

	release := make(chan struct{})
	defer close(release)
	blocked := make(chan struct{})
	n.enqueueInput("serial-a", "touch", "", func() error {
		close(blocked)
		<-release
		return nil
	})
	<-blocked

	n.enqueueInput("serial-a", "touch", "move:0:true", func() error { return nil })
	n.enqueueInput("serial-a", "touch", "move:1:true", func() error { return nil })
	n.enqueueInput("serial-a", "touch", "move:1:true", func() error { return nil })

	queue := n.inputQueue("serial-a")
	queue.mu.Lock()
	pending := len(queue.pending)
	queue.mu.Unlock()
	if pending != 2 {
		t.Fatalf("pending = %d, want 2 (one per pointer)", pending)
	}
}
