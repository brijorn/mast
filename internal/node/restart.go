package node

import (
	"time"
)

const restartDelay = 750 * time.Millisecond

func (n *Node) ScheduleRestart(delay time.Duration) error {
	if n.scheduleRestart == nil {
		return scheduleProcessRestartForPlatform(delay)
	}
	return n.scheduleRestart(delay)
}
