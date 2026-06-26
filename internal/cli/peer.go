package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/brijorn/mast/internal/peer"
)

type PeerCmd struct {
	Add PeerAddCmd `cmd:"" help:"Connect the running Mast node to a peer"`
	Ls  PeerLsCmd  `cmd:"" help:"List saved peers"`
}

type PeerAddCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
	APIAddr    string `name:"api" help:"Local Mast API base URL"`
	Target     string `arg:"" help:"Peer host, host:port, or websocket URL"`
}

type PeerLsCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
}

func (p *PeerAddCmd) Run() error {
	target, err := peer.NormalizeTarget(p.Target)
	if err != nil {
		return err
	}

	store, err := LoadPeerStore(p.ConfigPath)
	if err != nil {
		return err
	}
	added := addSavedPeer(store, target)
	if err := SavePeerStore(p.ConfigPath, store); err != nil {
		return err
	}

	apiBase, err := apiBaseURL(p.ConfigPath, p.APIAddr)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]string{"target": target})
	if err != nil {
		return err
	}

	res, err := http.Post(apiBase+"/api/peers", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() {
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf("add peer: %s: %s", res.Status, strings.TrimSpace(string(msg)))
	}

	if !added {
		_, err = fmt.Fprintf(os.Stdout, "peer already saved %s\n", target)
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "connected peer %s\n", target)
	return err
}

func (p *PeerLsCmd) Run() error {
	store, err := LoadPeerStore(p.ConfigPath)
	if err != nil {
		return err
	}

	for _, savedPeer := range store.Peers {
		if _, err := fmt.Fprintln(os.Stdout, savedPeer); err != nil {
			return err
		}
	}
	return nil
}

func apiBaseURL(configPath string, apiAddr string) (string, error) {
	if apiAddr != "" {
		return normalizeHTTPBase(apiAddr)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		return "", err
	}
	return normalizeHTTPBase(cfg.APIAddr)
}

func normalizeHTTPBase(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("api address required")
	}

	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}

	u, err := url.Parse(addr)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("api address scheme must be http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("api address host required")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}
