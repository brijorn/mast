# CLI

Mast commands configure and control the local machine. Commands that affect a
running node, such as `peer add`, talk to the local Mast HTTP API.

## config init

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
  "node_id": "",
  "bind_addr": ":6270",
  "proxy_addr": ":6272",
  "api_addr": ":6271",
  "advertise_host": "127.0.0.1",
  "android_enabled": false,
  "ios_enabled": false,
  "proxy_enabled": false
}
```

## config set

Updates a single configuration value. If the config file does not exist, this
command creates it first.

```sh
mast config set node_id pixel-proxy
mast config set advertise_host 100.64.0.10
mast config set android_enabled true
mast config set ios_enabled true
mast config set proxy_enabled true
mast config set runners.windows wine
mast config set runners..py "python3 -u"
```

Supported keys:

```text
node_id
bind_addr
proxy_addr
api_addr
advertise_host
android_enabled
ios_enabled
proxy_enabled
runners.<platform>
runners.<file_extension>
```

## config show

Prints the current configuration as JSON.

```sh
mast config show
```

## config path

Prints the default configuration path.

```sh
mast config path
```

## start

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

When started, the node ID uses `node_id` when configured. If `node_id` is
blank, Mast falls back to the host name returned by the operating system.

## peer add

Saves a peer in the peer store and asks the running local Mast node to connect
to it.

```sh
mast peer add 100.64.0.20
mast peer add 100.64.0.20:6270
mast peer add ws://100.64.0.20:6270/ws
```

If the port is omitted, Mast uses the peer websocket default port `6270`. If
the websocket path is omitted, Mast uses `/ws`.

Use `--api` if the local Mast API is not listening at the configured
`api_addr`:

```sh
mast peer add 100.64.0.20 --api http://127.0.0.1:6271
```

Saved peers are written to `peers.json` beside `config.json` and reconnected
when `mast start` runs.

## peer ls

Lists peers saved in `peers.json`.

```sh
mast peer ls
```

## version

Prints the current Mast version.

```sh
mast version
mast version --verbose
```

## update check

Checks whether the local Mast node has an available GitHub Release update.

```sh
mast update check
```

To check a connected peer through the local Mast node:

```sh
mast update check node-b
```

Use `--api` if the local Mast API is not listening at the configured
`api_addr`:

```sh
mast update check --api http://127.0.0.1:6271
```

## update apply

Applies an available GitHub Release update to the local Mast node.

```sh
mast update apply
```

To ask a connected peer to update itself through the local Mast node:

```sh
mast update apply node-b
```

Use `--force` to apply the latest release even when the current version matches
the latest version:

```sh
mast update apply --force
```

Use `--restart` to relaunch Mast after a successful update:

```sh
mast update apply --force --restart node-b
```

## service install

Installs Mast as an OS user service that runs `mast start`.

```sh
mast service install
```

Platform behavior:

- macOS: writes `~/Library/LaunchAgents/com.brijorn.mast.plist` and loads it with `launchctl`.
- Linux: writes `~/.config/systemd/user/mast.service` and enables it with `systemctl --user enable --now mast.service`.
- Windows: writes a scheduled task XML file under the user's Startup programs directory and creates a `mast` scheduled task with `schtasks`.

## service stop

Stops the installed service.

```sh
mast service stop
```

## service uninstall

Stops and removes the installed service.

```sh
mast service uninstall
```
