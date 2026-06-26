package node

import (
	"log"
	"os"
	"os/exec"
	"time"
)

const restartDelay = 750 * time.Millisecond

var scheduleProcessRestart = func(delay time.Duration) error {
	if delay <= 0 {
		delay = restartDelay
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := append([]string(nil), os.Args[1:]...)

	time.AfterFunc(delay, func() {
		cmd := exec.Command(executable, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Start(); err != nil {
			log.Println("restart:", err)
			return
		}
		os.Exit(0)
	})

	return nil
}

func (n *Node) ScheduleRestart(delay time.Duration) error {
	return scheduleProcessRestart(delay)
}
