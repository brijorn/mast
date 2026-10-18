package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	mastconfig "github.com/brijorn/mast/internal/config"
)

var runADBServerCommand = runADBServerCommandDefault

// How many times, and how far apart, a start is checked for a server that
// answers.
var (
	adbServerStartChecks        = 5
	adbServerStartCheckInterval = time.Second
)

func (n *Node) EnsureADBServer(ctx context.Context) error {
	n.mu.RLock()
	cfg := mastconfig.Config{
		AndroidEnabled: n.AndroidEnabled,
		ADBPort:        n.ADBPort,
		AdvertiseHost:  n.AdvertiseHost,
	}
	n.mu.RUnlock()
	return n.EnsureADBServerForConfig(ctx, cfg)
}

// EnsureADBServerForConfig makes sure a loopback-only adb server answers on
// the configured port and that the relay peers reach it through is running.
func (n *Node) EnsureADBServerForConfig(ctx context.Context, cfg mastconfig.Config) error {
	if !cfg.AndroidEnabled {
		n.stopADBRelay()
		return nil
	}

	port := cfg.ADBPort
	if port <= 0 {
		port = 5037
	}
	// The relay only pipes to the loopback port, so it runs whatever state the
	// server is in at this moment.
	defer n.ensureADBRelay(cfg.AdvertiseHost, port)

	// A server already answering is kept -- one a client started is as good as
	// one Mast starts -- unless it listens on every interface: an older Mast
	// started it with -a, and it would hand the whole network a shell.
	err := checkADBServer(ctx, "127.0.0.1", port)
	if err == nil && adbServerExposed(cfg.AdvertiseHost, port) {
		log.Printf("adb server on port %d listens on every interface; restarting it on loopback only", port)
		err = errors.New("adb server exposed")
	}
	if err == nil {
		return nil
	}
	_, _ = runADBServerCommand(ctx, "-P", strconv.Itoa(port), "kill-server")
	_, startErr := runADBServerCommand(ctx, "-P", strconv.Itoa(port), "start-server")
	// Any adb client run meanwhile -- a program's, or this node's own device
	// listing -- starts a server too, and whichever binds the port first wins:
	// the losers fail with "Address already in use". What matters is that one
	// answers, so a failed start is checked rather than believed.
	for attempt := 0; ; attempt++ {
		err = checkADBServer(ctx, "127.0.0.1", port)
		if err == nil {
			return nil
		}
		if attempt == adbServerStartChecks-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(adbServerStartCheckInterval):
		}
	}
	if startErr != nil {
		return fmt.Errorf("start adb server on port %d: %w", port, startErr)
	}
	return fmt.Errorf("adb server on 127.0.0.1:%d is not reachable: %w", port, err)
}

func checkADBServer(ctx context.Context, host string, port int) error {
	_, err := runADBServerCommand(ctx, "-H", host, "-P", strconv.Itoa(port), "devices")
	return err
}

func runADBServerCommandDefault(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := adbContext(ctx, adbCommandTimeout)
	defer cancel()

	output, err := execADBCommand(ctx, "adb", args...).CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return output, commandError("adb", args, output, err)
	}
	return output, nil
}
