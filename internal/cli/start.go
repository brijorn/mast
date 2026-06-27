package cli

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/brijorn/mast/internal/api"
	"github.com/brijorn/mast/internal/node"
	"github.com/brijorn/mast/internal/program"
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

	mastNode, err := node.NewNode(id, cfg.BindAddr, cfg.AdvertiseHost, cfg.AndroidEnabled, cfg.ProxyEnabled)
	if err != nil {
		return err
	}

	if cfg.ADBPort > 0 {
		mastNode.ADBPort = cfg.ADBPort
	}

	proxyRuntime := &runtimeProxy{}
	if cfg.ProxyEnabled {
		if err := proxyRuntime.Ensure(cfg.ProxyAddr); err != nil {
			return err
		}
	}
	programsDir := cfg.ProgramsDir
	if programsDir == "" {
		programsDir, err = DefaultProgramsPath()
		if err != nil {
			return err
		}
	}
	programStore, err := program.NewStore(programsDir, mastNode)
	if err != nil {
		return err
	}
	programStore.SetRunners(cfg.Runners)
	mastNode.SetConfig(s.ConfigPath, *cfg, &runtimeConfigApplier{programs: programStore, proxy: proxyRuntime})
	apiServer := api.NewServer(mastNode, programStore)
	var shuttingDown atomic.Bool
	stopSignals := make(chan os.Signal, 1)
	signal.Notify(stopSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopSignals)
	go func() {
		<-stopSignals
		shuttingDown.Store(true)
		programStore.Shutdown()
		if err := mastNode.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Println("node shutdown:", err)
		}
	}()

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

	if err := mastNode.Listen(); err != nil {
		if shuttingDown.Load() || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
	return nil
}

type runtimeConfigApplier struct {
	programs interface {
		SetRunners(map[string]string)
	}
	proxy *runtimeProxy
}

func (a *runtimeConfigApplier) ApplyRuntimeConfig(cfg Config, changedKeys []string) error {
	if a.programs != nil {
		a.programs.SetRunners(cfg.Runners)
	}
	if cfg.ProxyEnabled && a.proxy != nil {
		return a.proxy.Ensure(cfg.ProxyAddr)
	}
	return nil
}

type runtimeProxy struct {
	mu      sync.Mutex
	running bool
	addr    string
}

func (p *runtimeProxy) Ensure(addr string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return nil
	}
	server := proxy.NewServer(addr)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	p.running = true
	p.addr = addr
	go func() {
		if err := http.Serve(listener, server.Handler()); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println("proxy server listen err:", err)
		}
	}()
	return nil
}
