package cli

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/brijorn/mast/internal/api"
	"github.com/brijorn/mast/internal/node"
	"github.com/brijorn/mast/internal/proxy"
)

type StartCmd struct {
	ConfigPath string `name:"config" short:"c" help:"Path to config file" type:"path"`
}

func (s *StartCmd) Run() error {

	id, err := os.Hostname()
	if err != nil {
		return err
	}

	cfg, err := LoadConfig(s.ConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			path, pathErr := resolvePath(s.ConfigPath)
			if pathErr != nil {
				return pathErr
			}
			return fmt.Errorf("config not found at %s; run `mast config init`", path)
		}
		return err
	}

	mastNode, err := node.NewNode(id, cfg.BindAddr, cfg.AdvertiseHost, cfg.AndroidEnabled)
	if err != nil {
		return err
	}

	if cfg.ProxyEnabled {
		proxyServer := proxy.NewServer(cfg.ProxyAddr)
		go func() {
			if err := proxyServer.Listen(); err != nil {
				log.Println("proxy server listen err:", err)
			}
		}()
	}
	apiServer := api.NewServer(mastNode)

	go func() {
		if err := apiServer.Listen(cfg.APIAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println("api listener:", err)
		}
	}()

	peerStore, err := LoadPeerStore(s.ConfigPath)
	if err != nil {
		log.Println("load saved peers:", err)
	} else {
		for _, peerURL := range peerStore.Peers {
			if err := mastNode.Connect(peerURL); err != nil {
				log.Println("connect saved peer:", err)
			}
		}
	}

	return mastNode.Listen()
}
