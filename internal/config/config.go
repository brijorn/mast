package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	FileName         = "config.json"
	FileDir          = ".mast"
	ProgramsFileDir  = "programs"
	DefaultBindAddr  = ":6270"
	DefaultAPIAddr   = ":6271"
	DefaultProxyAddr = ":6272"
)

type BatteryProtection struct {
	Enabled       bool `json:"enabled"`
	MinPercent    int  `json:"min_percent"`
	ResumePercent int  `json:"resume_percent"`
	StopProgram   bool `json:"stop_program"`
	StopStream    bool `json:"stop_stream"`
	SendHome      bool `json:"send_home"`
}

type Config struct {
	NodeID            string            `json:"node_id"`
	BindAddr          string            `json:"bind_addr"`
	ProxyAddr         string            `json:"proxy_addr"`
	APIAddr           string            `json:"api_addr"`
	AdvertiseHost     string            `json:"advertise_host"`
	ADBPort           int               `json:"adb_port"`
	ProgramsDir       string            `json:"programs_dir"`
	AndroidEnabled    bool              `json:"android_enabled"`
	ProxyEnabled      bool              `json:"proxy_enabled"`
	LockPortrait      bool              `json:"lock_portrait"`
	BatteryProtection BatteryProtection `json:"battery_protection"`
	Runners           map[string]string `json:"runners,omitempty"`
}

type UpdateResult struct {
	Config              Config   `json:"config"`
	ChangedKeys         []string `json:"changed_keys"`
	RestartRequired     bool     `json:"restart_required"`
	RestartRequiredKeys []string `json:"restart_required_keys"`
}

func DefaultBatteryProtection() BatteryProtection {
	return BatteryProtection{
		Enabled:       false,
		MinPercent:    20,
		ResumePercent: 50,
		StopProgram:   true,
		StopStream:    true,
		SendHome:      true,
	}
}

func normalizeBatteryProtection(cfg BatteryProtection) BatteryProtection {
	defaults := DefaultBatteryProtection()
	if cfg.MinPercent == 0 {
		cfg.MinPercent = defaults.MinPercent
	}
	if cfg.ResumePercent == 0 {
		cfg.ResumePercent = defaults.ResumePercent
	}
	return cfg
}

func (c *Config) Set(key string, value string) error {
	if strings.HasPrefix(key, "runners.") {
		if c.Runners == nil {
			c.Runners = make(map[string]string)
		}
		target := strings.TrimPrefix(key, "runners.")
		if target == "" {
			return fmt.Errorf("invalid runner key")
		}
		if value == "" {
			delete(c.Runners, target)
		} else {
			c.Runners[target] = value
		}
		return nil
	}

	switch key {
	case "node_id":
		c.NodeID = strings.TrimSpace(value)
	case "bind_addr":
		c.BindAddr = value
	case "proxy_addr":
		c.ProxyAddr = value
	case "api_addr":
		c.APIAddr = value
	case "advertise_host":
		c.AdvertiseHost = value
	case "adb_port":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		c.ADBPort = parsed
	case "programs_dir":
		c.ProgramsDir = value
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
	case "lock_portrait":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.LockPortrait = parsed
	case "battery_protection.enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.BatteryProtection.Enabled = parsed
	case "battery_protection.min_percent":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		if parsed < 1 || parsed > 100 {
			return fmt.Errorf("battery_protection.min_percent must be between 1 and 100")
		}
		c.BatteryProtection.MinPercent = parsed
	case "battery_protection.resume_percent":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		if parsed < 1 || parsed > 100 {
			return fmt.Errorf("battery_protection.resume_percent must be between 1 and 100")
		}
		c.BatteryProtection.ResumePercent = parsed
	case "battery_protection.stop_program":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.BatteryProtection.StopProgram = parsed
	case "battery_protection.stop_stream":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.BatteryProtection.StopStream = parsed
	case "battery_protection.send_home":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.BatteryProtection.SendHome = parsed
	default:
		return fmt.Errorf("invalid config key: %s", key)
	}

	return nil
}

func (c Config) Clone() Config {
	c.BatteryProtection = normalizeBatteryProtection(c.BatteryProtection)
	clone := c
	if c.Runners != nil {
		clone.Runners = make(map[string]string, len(c.Runners))
		for key, value := range c.Runners {
			clone.Runners[key] = value
		}
	}
	return clone
}

func ApplyValues(current Config, values map[string]string) (Config, []string, []string, error) {
	next := current.Clone()
	keys := sortedKeys(values)
	for _, key := range keys {
		if err := next.Set(key, values[key]); err != nil {
			return Config{}, nil, nil, err
		}
	}

	changed := changedKeys(current, next, keys)
	restartKeys := restartRequiredKeys(changed)
	return next, changed, restartKeys, nil
}

func Save(path string, cfg *Config) error {
	path, err := ResolvePath(path)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	success = true
	return nil
}

func Load(path string) (*Config, error) {
	path, err := ResolvePath(path)
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

func LoadOrCreate(path string) (*Config, error) {
	cfg, err := Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CreateDefault(path)
		}
		return nil, err
	}
	return cfg, nil
}

func Default() Config {
	programsDir, err := DefaultProgramsPath()
	if err != nil {
		programsDir = filepath.Join(FileDir, ProgramsFileDir)
	}
	return Config{
		BindAddr:          DefaultBindAddr,
		ProxyAddr:         DefaultProxyAddr,
		APIAddr:           DefaultAPIAddr,
		AdvertiseHost:     "127.0.0.1",
		ADBPort:           5037,
		ProgramsDir:       programsDir,
		AndroidEnabled:    false,
		ProxyEnabled:      false,
		BatteryProtection: DefaultBatteryProtection(),
	}
}

func CreateDefault(path string) (*Config, error) {
	path, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	return &cfg, Save(path, &cfg)
}

func ResolvePath(path string) (string, error) {
	if path == "" {
		defaultPath, err := DefaultPath()
		if err != nil {
			return "", err
		}
		path = defaultPath
	}
	return path, nil
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, FileDir, FileName), nil
}

func DefaultProgramsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, FileDir, ProgramsFileDir), nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func changedKeys(before Config, after Config, requested []string) []string {
	var changed []string
	for _, key := range requested {
		if strings.HasPrefix(key, "runners.") {
			runnerKey := strings.TrimPrefix(key, "runners.")
			if before.Runners[runnerKey] != after.Runners[runnerKey] {
				changed = append(changed, key)
			}
			continue
		}
		switch key {
		case "node_id":
			if before.NodeID != after.NodeID {
				changed = append(changed, key)
			}
		case "bind_addr":
			if before.BindAddr != after.BindAddr {
				changed = append(changed, key)
			}
		case "proxy_addr":
			if before.ProxyAddr != after.ProxyAddr {
				changed = append(changed, key)
			}
		case "api_addr":
			if before.APIAddr != after.APIAddr {
				changed = append(changed, key)
			}
		case "advertise_host":
			if before.AdvertiseHost != after.AdvertiseHost {
				changed = append(changed, key)
			}
		case "adb_port":
			if before.ADBPort != after.ADBPort {
				changed = append(changed, key)
			}
		case "programs_dir":
			if before.ProgramsDir != after.ProgramsDir {
				changed = append(changed, key)
			}
		case "android_enabled":
			if before.AndroidEnabled != after.AndroidEnabled {
				changed = append(changed, key)
			}
		case "proxy_enabled":
			if before.ProxyEnabled != after.ProxyEnabled {
				changed = append(changed, key)
			}
		case "lock_portrait":
			if before.LockPortrait != after.LockPortrait {
				changed = append(changed, key)
			}
		case "battery_protection.enabled":
			if before.BatteryProtection.Enabled != after.BatteryProtection.Enabled {
				changed = append(changed, key)
			}
		case "battery_protection.min_percent":
			if before.BatteryProtection.MinPercent != after.BatteryProtection.MinPercent {
				changed = append(changed, key)
			}
		case "battery_protection.resume_percent":
			if before.BatteryProtection.ResumePercent != after.BatteryProtection.ResumePercent {
				changed = append(changed, key)
			}
		case "battery_protection.stop_program":
			if before.BatteryProtection.StopProgram != after.BatteryProtection.StopProgram {
				changed = append(changed, key)
			}
		case "battery_protection.stop_stream":
			if before.BatteryProtection.StopStream != after.BatteryProtection.StopStream {
				changed = append(changed, key)
			}
		case "battery_protection.send_home":
			if before.BatteryProtection.SendHome != after.BatteryProtection.SendHome {
				changed = append(changed, key)
			}
		}
	}
	return changed
}

func restartRequiredKeys(changed []string) []string {
	restartKeys := make([]string, 0, len(changed))
	for _, key := range changed {
		switch key {
		case "node_id", "bind_addr", "api_addr", "proxy_addr", "programs_dir":
			restartKeys = append(restartKeys, key)
		}
	}
	return restartKeys
}
