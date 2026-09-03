package node

import (
	"log"
	"sync"
)

// inputQueueDepth bounds the input one device can have waiting.
//
// Moves collapse (see coalesceKey), so a drag never accumulates however far
// behind the device falls. What can still stack up is discrete gestures --
// swipes at a quarter second of pointer steps each -- and this bounds that
// backlog to a few seconds. Past it the sender waits, which is honest
// backpressure: a phone throttled too hard to keep up should say so rather
// than bank a gesture to replay long after the operator stopped asking.
const inputQueueDepth = 32

// inputJob is one queued operation for a device.
type inputJob struct {
	name string
	run  func() error
	// coalesceKey, when set, lets a newer job replace an identically keyed one
	// still waiting at the back of the queue. Only a pointer move earns this:
	// its whole content is "the finger is here now", so the newer one says
	// everything the older one did. A down, an up, or a discrete gesture never
	// coalesces -- dropping those changes what the device is told happened.
	coalesceKey string
}

// deviceInput is one device's queue and the worker draining it.
type deviceInput struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []inputJob
	closed  bool
	warned  bool
}

// enqueueInput hands work to the goroutine that owns this serial's input and
// returns without waiting for it to run.
//
// Input is the one class of peer message that must stay ordered: a drag's
// DOWN/MOVE/UP only means anything in sequence, so these cases cannot be
// dispatched with a bare `go` the way request/response messages are. They ran
// inline in the peer read loop instead, which made every device on a node wait
// out every swipe's 250ms of pointer steps -- a tap on one phone measured
// 2.17s behind eight swipes issued to a different phone on the same node.
//
// One worker per serial keeps the ordering that matters and drops the coupling
// that does not.
func (n *Node) enqueueInput(serial string, name string, coalesceKey string, run func() error) {
	queue := n.inputQueue(serial)
	job := inputJob{name: name, run: run, coalesceKey: coalesceKey}

	queue.mu.Lock()
	defer queue.mu.Unlock()

	if coalesceKey != "" && len(queue.pending) > 0 {
		if last := len(queue.pending) - 1; queue.pending[last].coalesceKey == coalesceKey {
			// Replace rather than append: the position this job carries
			// supersedes the one still waiting, and a device too slow to have
			// taken that one has no use for it now.
			queue.pending[last] = job
			queue.cond.Broadcast()
			return
		}
	}

	for len(queue.pending) >= inputQueueDepth && !queue.closed {
		if !queue.warned {
			queue.warned = true
			log.Println("input queue full for", serial, "- waiting to enqueue", name)
		}
		queue.cond.Wait()
	}
	if queue.closed {
		return
	}
	queue.warned = false

	queue.pending = append(queue.pending, job)
	queue.cond.Broadcast()
}

// inputQueue returns the serial's queue, starting its worker on first use.
func (n *Node) inputQueue(serial string) *deviceInput {
	n.inputMu.Lock()
	defer n.inputMu.Unlock()

	if n.inputWorkers == nil {
		n.inputWorkers = make(map[string]*deviceInput)
	}

	if queue, ok := n.inputWorkers[serial]; ok {
		return queue
	}

	queue := &deviceInput{}
	queue.cond = sync.NewCond(&queue.mu)
	n.inputWorkers[serial] = queue

	go n.runInputWorker(queue)
	go func() {
		<-n.ctx.Done()
		queue.mu.Lock()
		queue.closed = true
		queue.cond.Broadcast()
		queue.mu.Unlock()
	}()

	return queue
}

// runInputWorker drains one device's input in arrival order until the node
// shuts down. Workers are keyed by serial and live for the node's lifetime;
// the set is bounded by the devices that have ever been driven here.
func (n *Node) runInputWorker(queue *deviceInput) {
	for {
		queue.mu.Lock()
		for len(queue.pending) == 0 && !queue.closed {
			queue.cond.Wait()
		}
		if queue.closed {
			queue.mu.Unlock()
			return
		}

		job := queue.pending[0]
		queue.pending = queue.pending[1:]
		queue.cond.Broadcast()
		queue.mu.Unlock()

		if err := job.run(); err != nil {
			log.Println(job.name+":", err)
		}
	}
}
