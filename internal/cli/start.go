package cli

import (
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/brijorn/mast/internal/api"
	"github.com/brijorn/mast/internal/node"
	"github.com/brijorn/mast/internal/proxy"
)

type StartCmd struct {
	BindAddr       string `help:"Websocket listen address" default:":8080"`
	ProxyAddr      string `help:"Proxy listen address" default:":8888"`
	APIAddr        string `help:"Control API listen address" default:":8081"`
	AdvertiseHost  string `help:"Current node host" short:"h"`
	AndroidEnabled bool   `help:"Enable Android device support"`
}

func (s *StartCmd) Run() error {

	id, err := os.Hostname()
	if err != nil {
		return err
	}
	mastNode, err := node.NewNode(id, s.BindAddr, s.AdvertiseHost, s.AndroidEnabled)
	if err != nil {
		return err
	}
	proxyServer := proxy.NewServer(s.ProxyAddr)
	apiServer := api.NewServer(mastNode)
	go func() {
		if err := mastNode.Listen(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println("node listener:", err)
		}
	}()
	go func() {
		if err := apiServer.Listen(s.APIAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println("api listener:", err)
		}
	}()

	return proxyServer.Listen()
}
