package api

import (
	"testing"
)

func movePointer(id uint64) controlWSRequest {
	return controlWSRequest{Type: "touch", Action: "move", PointerID: &id}
}

// The regression behind "control queue full, then it scrolls on its own": a
// drag outrunning the device banked every stale move and replayed them.
func TestControlWSQueueCoalescesPendingMoves(t *testing.T) {
	q := newControlWSQueue()

	for i := range 1000 {
		req := movePointer(0)
		req.Y = i
		if !q.push(req) {
			t.Fatalf("push %d rejected; moves should collapse, not fill", i)
		}
	}

	q.mu.Lock()
	pending := len(q.pending)
	q.mu.Unlock()
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}

	req, ok := q.pop()
	if !ok {
		t.Fatal("pop reported closed")
	}
	if req.Y != 999 {
		t.Fatalf("kept move y=%d, want the newest (999)", req.Y)
	}
}

// Two fingers are two gestures; their moves must not collapse into each other.
func TestControlWSQueueCoalescesPerPointer(t *testing.T) {
	q := newControlWSQueue()
	q.push(movePointer(0))
	q.push(movePointer(1))
	q.push(movePointer(1))

	q.mu.Lock()
	pending := len(q.pending)
	q.mu.Unlock()
	if pending != 2 {
		t.Fatalf("pending = %d, want 2", pending)
	}
}

// Coalescing must not reach past other input: a move either side of an UP
// belongs to a different moment of the gesture.
func TestControlWSQueueDoesNotCoalesceAcrossOtherInput(t *testing.T) {
	q := newControlWSQueue()
	q.push(movePointer(0))
	q.push(controlWSRequest{Type: "touch", Action: "up"})
	q.push(movePointer(0))

	q.mu.Lock()
	pending := len(q.pending)
	q.mu.Unlock()
	if pending != 3 {
		t.Fatalf("pending = %d, want 3", pending)
	}
}

// Input that cannot coalesce still has to report backpressure rather than grow
// without bound.
func TestControlWSQueueRejectsWhenFullOfDiscreteInput(t *testing.T) {
	q := newControlWSQueue()
	for i := range controlWSQueueSize {
		if !q.push(controlWSRequest{Type: "tap"}) {
			t.Fatalf("push %d rejected before the queue was full", i)
		}
	}
	if q.push(controlWSRequest{Type: "tap"}) {
		t.Fatal("full queue accepted another tap")
	}
}

// Order is preserved for everything that does not coalesce.
func TestControlWSQueuePreservesOrder(t *testing.T) {
	q := newControlWSQueue()
	for i := range 10 {
		q.push(controlWSRequest{Type: "tap", X: i})
	}
	for i := range 10 {
		req, ok := q.pop()
		if !ok {
			t.Fatal("pop reported closed early")
		}
		if req.X != i {
			t.Fatalf("got x=%d at position %d", req.X, i)
		}
	}
}

// A closed queue wakes its consumer instead of leaving it parked.
func TestControlWSQueueCloseReleasesPop(t *testing.T) {
	q := newControlWSQueue()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := q.pop(); ok {
			t.Error("pop returned a request from a closed queue")
		}
	}()
	q.close()
	<-done
}
