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
		n.enqueueInput("serial-a", "touch", func() error {
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
	n.enqueueInput("serial-slow", "swipe", func() error {
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
	n.enqueueInput("serial-other", "tap", func() error {
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
	n.enqueueInput("serial-a", "swipe", func() error {
		<-release
		return nil
	})

	returned := make(chan struct{})
	go func() {
		n.enqueueInput("serial-a", "tap", func() error { return nil })
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

	n.enqueueInput("serial-a", "touch", func() error { return errors.New("no control connection") })

	ran := make(chan struct{})
	n.enqueueInput("serial-a", "touch", func() error {
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

	queue := n.inputQueue("serial-a")

	// Cancel against an empty queue so the worker's select has exactly one
	// ready case and has to take it.
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Nothing drains the queue once the worker has returned, so it fills and
	// stays full.
	for range inputQueueDepth {
		select {
		case queue <- func() {}:
		default:
			t.Fatal("queue full early: worker drained nothing it was given")
		}
	}

	select {
	case queue <- func() {}:
		t.Fatal("worker still draining after the node stopped")
	default:
	}
}
