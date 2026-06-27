package cli

import (
	"encoding/json"
	"fmt"
	"os"

	mastconfig "github.com/brijorn/mast/internal/config"
)

const (
	ConfigFileName  = mastconfig.FileName
	ConfigFileDir   = mastconfig.FileDir
	ProgramsFileDir = mastconfig.ProgramsFileDir
)

type Config = mastconfig.Config

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

func resolvePath(path string) (string, error) {
	return mastconfig.ResolvePath(path)
}

func SaveConfig(path string, config *Config) error {
	return mastconfig.Save(path, config)
}

func LoadConfig(path string) (*Config, error) {
	return mastconfig.Load(path)
}

func LoadOrCreateConfig(path string) (*Config, error) {
	return mastconfig.LoadOrCreate(path)
}

func DefaultConfigPath() (string, error) {
	return mastconfig.DefaultPath()
}

func DefaultConfig() Config {
	return mastconfig.Default()
}

func DefaultProgramsPath() (string, error) {
	return mastconfig.DefaultProgramsPath()
}

func CreateDefaultConfig(path string) (*Config, error) {
	return mastconfig.CreateDefault(path)
}
