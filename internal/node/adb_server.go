package node

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	mastconfig "github.com/brijorn/mast/internal/config"
)

var runADBServerCommand = runADBServerCommandDefault

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

func (n *Node) EnsureADBServerForConfig(ctx context.Context, cfg mastconfig.Config) error {
	if !cfg.AndroidEnabled {
		return nil
	}

	port := cfg.ADBPort
	if port <= 0 {
		port = 5037
	}
	host := adbServerHost(cfg.AdvertiseHost)
	if err := checkADBServer(ctx, host, port); err == nil {
		return nil
	}

	// If a localhost-only server owns the port, replace it with one that accepts
	// connections from the advertised host. Programs on other Mast nodes use this
	// endpoint through ADB_SERVER_SOCKET.
	_, _ = runADBServerCommand(ctx, "-P", strconv.Itoa(port), "kill-server")
	if _, err := runADBServerCommand(ctx, "-a", "-P", strconv.Itoa(port), "start-server"); err != nil {
		return fmt.Errorf("start adb server on port %d: %w", port, err)
	}
	if err := checkADBServer(ctx, host, port); err != nil {
		return fmt.Errorf("adb server on %s:%d is not reachable: %w", host, port, err)
	}
	return nil
}

func adbServerHost(advertiseHost string) string {
	host := strings.TrimSpace(advertiseHost)
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimSuffix(host, "/")
	if before, _, ok := strings.Cut(host, ":"); ok {
		host = before
	}
	if host == "" {
		return "127.0.0.1"
	}
	return host
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
