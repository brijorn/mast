package cli

import (
	"fmt"
	"os"
	"strings"

	mastconfig "github.com/brijorn/mast/internal/config"
)

type DeviceCmd struct {
	Blacklist DeviceBlacklistCmd `cmd:"" help:"Manage the startup device blacklist"`
}

type DeviceBlacklistCmd struct {
	Ls     DeviceBlacklistLsCmd     `cmd:"" help:"List blacklisted device serials"`
	Add    DeviceBlacklistAddCmd    `cmd:"" help:"Add a device serial to the blacklist"`
	Remove DeviceBlacklistRemoveCmd `cmd:"" help:"Remove a device serial from the blacklist"`
	Clear  DeviceBlacklistClearCmd  `cmd:"" help:"Clear the device blacklist"`
}

type DeviceBlacklistLsCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
}

type DeviceBlacklistAddCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
	Serial     string `arg:"" help:"Device serial or iOS UDID"`
}

type DeviceBlacklistRemoveCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
	Serial     string `arg:"" help:"Device serial or iOS UDID"`
}

type DeviceBlacklistClearCmd struct {
	ConfigPath string `name:"config" short:"c" type:"path" help:"Path to config file"`
}

func (c *DeviceBlacklistLsCmd) Run() error {
	cfg, err := LoadConfig(c.ConfigPath)
	if err != nil {
		return err
	}
	for _, serial := range mastconfig.NormalizeDeviceBlacklist(cfg.DeviceBlacklist) {
		if _, err := fmt.Fprintln(os.Stdout, serial); err != nil {
			return err
		}
	}
	return nil
}

func (c *DeviceBlacklistAddCmd) Run() error {
	cfg, err := LoadOrCreateConfig(c.ConfigPath)
	if err != nil {
		return err
	}
	serial, err := requiredSerial(c.Serial)
	if err != nil {
		return err
	}
	before := mastconfig.FormatDeviceBlacklist(cfg.DeviceBlacklist)
	cfg.DeviceBlacklist = mastconfig.AddDeviceBlacklist(cfg.DeviceBlacklist, serial)
	if err := SaveConfig(c.ConfigPath, cfg); err != nil {
		return err
	}
	if before == mastconfig.FormatDeviceBlacklist(cfg.DeviceBlacklist) {
		_, err = fmt.Fprintf(os.Stdout, "device already blacklisted %s\n", serial)
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "blacklisted device %s; restart Mast to apply\n", serial)
	return err
}

func (c *DeviceBlacklistRemoveCmd) Run() error {
	cfg, err := LoadOrCreateConfig(c.ConfigPath)
	if err != nil {
		return err
	}
	serial, err := requiredSerial(c.Serial)
	if err != nil {
		return err
	}
	before := mastconfig.FormatDeviceBlacklist(cfg.DeviceBlacklist)
	cfg.DeviceBlacklist = mastconfig.RemoveDeviceBlacklist(cfg.DeviceBlacklist, serial)
	if err := SaveConfig(c.ConfigPath, cfg); err != nil {
		return err
	}
	if before == mastconfig.FormatDeviceBlacklist(cfg.DeviceBlacklist) {
		_, err = fmt.Fprintf(os.Stdout, "device was not blacklisted %s\n", serial)
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "removed device %s from blacklist; restart Mast to apply\n", serial)
	return err
}

func (c *DeviceBlacklistClearCmd) Run() error {
	cfg, err := LoadOrCreateConfig(c.ConfigPath)
	if err != nil {
		return err
	}
	changed := len(mastconfig.NormalizeDeviceBlacklist(cfg.DeviceBlacklist)) > 0
	cfg.DeviceBlacklist = nil
	if err := SaveConfig(c.ConfigPath, cfg); err != nil {
		return err
	}
	if !changed {
		_, err = fmt.Fprintln(os.Stdout, "device blacklist already empty")
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, "cleared device blacklist; restart Mast to apply")
	return err
}

func requiredSerial(serial string) (string, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return "", fmt.Errorf("serial required")
	}
	return serial, nil
}
