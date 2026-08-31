package node

import "log"

// inputQueueDepth bounds one device's pending input. A drag delivers roughly
// sixty moves a second and a single swipe occupies the device for a quarter
// second, so a backlog this deep already means the gesture behind it is long
// past mattering. The cap exists to make that visible, not to be reached.
const inputQueueDepth = 128

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
func (n *Node) enqueueInput(serial string, name string, run func() error) {
	queue := n.inputQueue(serial)
	job := func() {
		if err := run(); err != nil {
			log.Println(name+":", err)
		}
	}

	select {
	case queue <- job:
		return
	default:
	}

	// Never discard the overflow: a dropped UP strands the pointer down, which
	// reads as a wedged device long after the burst that caused it. Waiting
	// here stalls this peer loop the way the old code always did, but only
	// while a single device is this far behind.
	log.Println("input queue full for", serial, "- waiting to enqueue", name)
	select {
	case queue <- job:
	case <-n.ctx.Done():
	}
}

// inputQueue returns the serial's queue, starting its worker on first use.
func (n *Node) inputQueue(serial string) chan func() {
	n.inputMu.Lock()
	defer n.inputMu.Unlock()

	if n.inputWorkers == nil {
		n.inputWorkers = make(map[string]chan func())
	}

	if queue, ok := n.inputWorkers[serial]; ok {
		return queue
	}

	queue := make(chan func(), inputQueueDepth)
	n.inputWorkers[serial] = queue
	go n.runInputWorker(queue)

	return queue
}

// runInputWorker drains one device's input in arrival order until the node
// shuts down. Workers are keyed by serial and live for the node's lifetime;
// the set is bounded by the devices that have ever been driven here.
func (n *Node) runInputWorker(queue chan func()) {
	for {
		select {
		case job := <-queue:
			job()
		case <-n.ctx.Done():
			return
		}
	}
}
