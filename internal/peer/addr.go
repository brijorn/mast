package peer

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const DefaultPort = "6270"

func NormalizeTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("peer target required")
	}

	if !strings.Contains(target, "://") {
		target = "ws://" + target
	}

	u, err := url.Parse(target)
	if err != nil {
		return "", err
	}

	if u.Scheme != "ws" && u.Scheme != "wss" {
		return "", fmt.Errorf("peer target scheme must be ws or wss")
	}

	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("peer target host required")
	}

	port := u.Port()
	if port == "" {
		port = DefaultPort
	}

	u.Host = net.JoinHostPort(host, port)
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws"
	}

	return u.String(), nil
}
