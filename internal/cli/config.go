package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const (
	ConfigFileName   = "config.json"
	ConfigFileDir    = ".mast"
	defaultBindAddr  = ":6270"
	defaultAPIAddr   = ":6271"
	defaultProxyAddr = ":6272"
)

type ConfigCmd struct {
	Init ConfigInitCmd `cmd:"" help:"Create default configuration"`
	Path ConfigPathCmd `cmd:"" help:"Config path"`
	Show ConfigShowCmd `cmd:"" help:"Show configuration"`
	Set  ConfigSetCmd  `cmd:"" help:"Set configuration"`
}

type ConfigInitCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
	Force      bool   `help:"Overwrite existing config file"`
}

func (c *ConfigInitCmd) Run() error {
	path, err := resolvePath(c.ConfigPath)
	if err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil && !c.Force {
		return fmt.Errorf("config already exists at %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	if _, err := CreateDefaultConfig(c.ConfigPath); err != nil {
		return err
	}

	_, err = fmt.Fprintf(os.Stdout, "created config at %s\n", path)
	if err != nil {
		return err
	}

	return nil
}

type ConfigPathCmd struct{}

func (c *ConfigPathCmd) Run() error {
	path, err := DefaultConfigPath()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, path)
	if err != nil {
		return err
	}

	return nil
}

type ConfigShowCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
}

func (c *ConfigShowCmd) Run() error {
	cfg, err := LoadConfig(c.ConfigPath)
	if err != nil {
		return err
	}

	return json.NewEncoder(os.Stdout).Encode(cfg)
}

type ConfigSetCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
	Key        string `arg:"" help:"Set key"`
	Value      string `arg:"" help:"Set value"`
}

func (c *ConfigSetCmd) Run() error {
	cfg, err := LoadOrCreateConfig(c.ConfigPath)
	if err != nil {
		return err
	}

	if err := cfg.Set(c.Key, c.Value); err != nil {
		return err
	}
	return SaveConfig(c.ConfigPath, cfg)
}

type Config struct {
	BindAddr       string `json:"bind_addr"`
	ProxyAddr      string `json:"proxy_addr"`
	APIAddr        string `json:"api_addr"`
	AdvertiseHost  string `json:"advertise_host"`
	AndroidEnabled bool   `json:"android_enabled"`
	ProxyEnabled   bool   `json:"proxy_enabled"`
}

func (c *Config) Set(key string, value string) error {
	switch key {
	case "bind_addr":
		c.BindAddr = value
	case "proxy_addr":
		c.ProxyAddr = value
	case "api_addr":
		c.APIAddr = value
	case "advertise_host":
		c.AdvertiseHost = value
	case "android_enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.AndroidEnabled = parsed
	case "proxy_enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.ProxyEnabled = parsed

	default:
		return fmt.Errorf("invalid config key: %s", key)
	}

	return nil
}

func resolvePath(path string) (string, error) {
	if path == "" {
		defaultPath, err := DefaultConfigPath()
		if err != nil {
			return "", err
		}
		path = defaultPath
	}

	return path, nil
}

func SaveConfig(path string, config *Config) error {
	path, err := resolvePath(path)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, encoded, 0600)
}
func LoadConfig(path string) (*Config, error) {
	path, err := resolvePath(path)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func(f *os.File) {
		_ = f.Close()
	}(f)

	var cfg Config
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func LoadOrCreateConfig(path string) (*Config, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CreateDefaultConfig(path)
		}
		return nil, err
	}

	return cfg, nil
}

func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigFileDir, ConfigFileName), nil
}

func DefaultConfig() Config {
	return Config{
		BindAddr:       defaultBindAddr,
		ProxyAddr:      defaultProxyAddr,
		APIAddr:        defaultAPIAddr,
		AdvertiseHost:  "127.0.0.1",
		AndroidEnabled: false,
		ProxyEnabled:   false,
	}
}

func CreateDefaultConfig(path string) (*Config, error) {
	path, err := resolvePath(path)
	if err != nil {
		return nil, err
	}

	cfg := DefaultConfig()
	return &cfg, SaveConfig(path, &cfg)
}
