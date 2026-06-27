package node

import (
	"time"
)

const restartDelay = 750 * time.Millisecond

var scheduleProcessRestart = scheduleProcessRestartForPlatform

func (n *Node) ScheduleRestart(delay time.Duration) error {
	return scheduleProcessRestart(delay)
}
