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

type Config struct {
	NodeID          string   `json:"node_id"`
	BindAddr        string   `json:"bind_addr"`
	ProxyAddr       string   `json:"proxy_addr"`
	APIAddr         string   `json:"api_addr"`
	AdvertiseHost   string   `json:"advertise_host"`
	ADBPort         int      `json:"adb_port"`
	ProgramsDir     string   `json:"programs_dir"`
	DeviceBlacklist []string `json:"device_blacklist,omitempty"`
	AndroidEnabled  bool     `json:"android_enabled"`
	IOSEnabled      bool     `json:"ios_enabled"`
	ProxyEnabled    bool     `json:"proxy_enabled"`
	LockPortrait    bool     `json:"lock_portrait"`
	KeepDisplayOff  bool     `json:"keep_display_off"`

	// Address family the proxy dials out on: "ipv4", "ipv6", or "auto".
	// Defaults to IPv4 because a proxy's job is a stable outward identity and
	// a dual-stack cellular link does not have one.
	ProxyAddressFamily string `json:"proxy_address_family"`

	// Encoder settings for viewer streams. They take effect on the next stream
	// a device starts, so a new value can be tried by updating config and
	// reopening the stream rather than rebuilding the caller.
	StreamMaxSize           int    `json:"stream_max_size"`
	StreamVideoBitrate      int    `json:"stream_video_bitrate"`
	StreamVideoCodecOptions string `json:"stream_video_codec_options"`

	Runners map[string]string `json:"runners,omitempty"`
}

// StreamDefaults are the encoder settings a node uses when a caller does not
// specify its own. They match what the dashboard asked for before the settings
// became configurable, so an existing node keeps its current picture.
const (
	DefaultStreamMaxSize           = 1080
	DefaultStreamVideoBitrate      = 1_500_000
	DefaultStreamVideoCodecOptions = "i-frame-interval=1"
)

// Proxy address families. IPv4 is the default because carrier IPv4 is what
// upstream sites are reached by in practice, and an IPv6-only egress would
// strand IPv4-only destinations.
const (
	AddressFamilyIPv4 = "ipv4"
	AddressFamilyIPv6 = "ipv6"
	AddressFamilyAuto = "auto"

	DefaultProxyAddressFamily = AddressFamilyIPv4
)

func (c *Config) UnmarshalJSON(data []byte) error {
	type configAlias Config
	*c = Config{
		KeepDisplayOff:          true,
		ProxyAddressFamily:      DefaultProxyAddressFamily,
		StreamMaxSize:           DefaultStreamMaxSize,
		StreamVideoBitrate:      DefaultStreamVideoBitrate,
		StreamVideoCodecOptions: DefaultStreamVideoCodecOptions,
	}
	return json.Unmarshal(data, (*configAlias)(c))
}

type UpdateResult struct {
	Config              Config   `json:"config"`
	ChangedKeys         []string `json:"changed_keys"`
	RestartRequired     bool     `json:"restart_required"`
	RestartRequiredKeys []string `json:"restart_required_keys"`
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
	case "device_blacklist":
		c.DeviceBlacklist = ParseDeviceBlacklist(value)
	case "android_enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.AndroidEnabled = parsed
	case "ios_enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.IOSEnabled = parsed
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
	case "keep_display_off":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		c.KeepDisplayOff = parsed
	case "proxy_address_family":
		family := strings.ToLower(strings.TrimSpace(value))
		switch family {
		case AddressFamilyIPv4, AddressFamilyIPv6, AddressFamilyAuto:
		default:
			return fmt.Errorf("proxy_address_family must be ipv4, ipv6, or auto")
		}
		c.ProxyAddressFamily = family
	case "stream_max_size":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		if parsed < 0 {
			return fmt.Errorf("stream_max_size must not be negative")
		}
		c.StreamMaxSize = parsed
	case "stream_video_bitrate":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		if parsed < 0 {
			return fmt.Errorf("stream_video_bitrate must not be negative")
		}
		c.StreamVideoBitrate = parsed
	case "stream_video_codec_options":
		c.StreamVideoCodecOptions = strings.TrimSpace(value)
	default:
		return fmt.Errorf("invalid config key: %s", key)
	}

	return nil
}

func (c Config) Clone() Config {
	clone := c
	clone.DeviceBlacklist = append([]string(nil), c.DeviceBlacklist...)
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
		BindAddr:       DefaultBindAddr,
		ProxyAddr:      DefaultProxyAddr,
		APIAddr:        DefaultAPIAddr,
		AdvertiseHost:  "127.0.0.1",
		ADBPort:        5037,
		ProgramsDir:    programsDir,
		AndroidEnabled: false,
		ProxyEnabled:   false,
		KeepDisplayOff: true,

		ProxyAddressFamily:      DefaultProxyAddressFamily,
		StreamMaxSize:           DefaultStreamMaxSize,
		StreamVideoBitrate:      DefaultStreamVideoBitrate,
		StreamVideoCodecOptions: DefaultStreamVideoCodecOptions,
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
		case "device_blacklist":
			if strings.Join(NormalizeDeviceBlacklist(before.DeviceBlacklist), "\x00") != strings.Join(NormalizeDeviceBlacklist(after.DeviceBlacklist), "\x00") {
				changed = append(changed, key)
			}
		case "android_enabled":
			if before.AndroidEnabled != after.AndroidEnabled {
				changed = append(changed, key)
			}
		case "ios_enabled":
			if before.IOSEnabled != after.IOSEnabled {
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
		case "keep_display_off":
			if before.KeepDisplayOff != after.KeepDisplayOff {
				changed = append(changed, key)
			}
		case "proxy_address_family":
			if before.ProxyAddressFamily != after.ProxyAddressFamily {
				changed = append(changed, key)
			}
		case "stream_max_size":
			if before.StreamMaxSize != after.StreamMaxSize {
				changed = append(changed, key)
			}
		case "stream_video_bitrate":
			if before.StreamVideoBitrate != after.StreamVideoBitrate {
				changed = append(changed, key)
			}
		case "stream_video_codec_options":
			if before.StreamVideoCodecOptions != after.StreamVideoCodecOptions {
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
		case "node_id", "bind_addr", "api_addr", "proxy_addr", "programs_dir", "device_blacklist":
			restartKeys = append(restartKeys, key)
		}
	}
	return restartKeys
}

func ParseDeviceBlacklist(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	return NormalizeDeviceBlacklist(parts)
}

func FormatDeviceBlacklist(serials []string) string {
	return strings.Join(NormalizeDeviceBlacklist(serials), ",")
}

func NormalizeDeviceBlacklist(serials []string) []string {
	seen := make(map[string]struct{}, len(serials))
	out := make([]string, 0, len(serials))
	for _, serial := range serials {
		serial = strings.TrimSpace(serial)
		if serial == "" {
			continue
		}
		if _, ok := seen[serial]; ok {
			continue
		}
		seen[serial] = struct{}{}
		out = append(out, serial)
	}
	sort.Strings(out)
	return out
}

func AddDeviceBlacklist(serials []string, serial string) []string {
	return NormalizeDeviceBlacklist(append(append([]string(nil), serials...), serial))
}

func RemoveDeviceBlacklist(serials []string, serial string) []string {
	serial = strings.TrimSpace(serial)
	out := make([]string, 0, len(serials))
	for _, existing := range NormalizeDeviceBlacklist(serials) {
		if existing == serial {
			continue
		}
		out = append(out, existing)
	}
	return out
}
