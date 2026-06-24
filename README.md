# Mast

An Android/IOS device control layer for coordinating devices
across multiple computers.

##### Current Capabilities:
- Discover Android devices through ADB (Peer devices are also shown)
- Connect to peer nodes over the local network
- Expose a local control API for device listing, scrcpy stream startup, taps, and swipes.
- Expose a proxy server on a port

The project runs as a lightweight program on each machine that owns devices, while a main node or dashboard can coordinate each node from one place. Intended for use in a private network or with Tailscale

## Documentation

- [Control API](docs/api.md)
- [Peer websocket protocol](docs/protocol.md)

## Control API

The local control API exposes endpoints for devices, streams, and Android input
commands.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/devices` | List visible Android devices |
| `POST` | `/api/streams` | Start a scrcpy stream |
| `POST` | `/api/control/tap` | Tap stream coordinates |
| `POST` | `/api/control/swipe` | Swipe stream coordinates |

See [docs/api.md](docs/api.md) for request and response bodies.

## Startup Commands

### config init

Creates a default configuration file.

```sh
mast config init
```

By default, Mast stores configuration at:

```text
~/.mast/config.json
```

Use `--config` to create a config somewhere else:

```sh
mast config init --config ./mast.dev.json
```

Use `--force` to overwrite an existing config.

Default configuration:

```json
{
  "bind_addr": ":6270",
  "proxy_addr": ":6272",
  "api_addr": ":6271",
  "advertise_host": "127.0.0.1",
  "android_enabled": false,
  "proxy_enabled": false
}
```

### config set

Updates a single configuration value. If the config file does not exist, this
command creates it first.

```sh
mast config set advertise_host 100.64.0.10
mast config set android_enabled true
mast config set proxy_enabled true
```

Supported keys:

```text
bind_addr
proxy_addr
api_addr
advertise_host
android_enabled
proxy_enabled
```

### config show

Prints the current configuration as JSON.

```sh
mast config show
```

### config path

Prints the default configuration path.

```sh
mast config path
```

### start

Runs the Mast node using the configured peer websocket address, control API
address, and optional proxy server.

```sh
mast start
```

Mast requires a config file before startup. Create one first with:

```sh
mast config init
```

Use `--config` to start from a non-default config path:

```sh
mast start --config ./mast.dev.json
```

When started, the node ID is the host name returned by the operating system.

### service install

Installs Mast as an OS user service that runs `mast start`.

```sh
mast service install
```

Platform behavior:

- macOS: writes `~/Library/LaunchAgents/com.brijorn.mast.plist` and loads it with `launchctl`.
- Linux: writes `~/.config/systemd/user/mast.service` and enables it with `systemctl --user enable --now mast.service`.
- Windows: writes a scheduled task XML file under the user's Startup programs directory and creates a `mast` scheduled task with `schtasks`.

### service stop

Stops the installed service.

```sh
mast service stop
```

### service uninstall

Stops and removes the installed service.

```sh
mast service uninstall
```
