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
  "adb_port": 5037,
  "programs_dir": "/home/user/.mast/programs",
  "android_enabled": false,
  "ios_enabled": false,
  "proxy_enabled": false,
  "lock_portrait": false,
  "keep_display_off": true,
  "stream_idle_timeout": 300
}
```

## config set

Updates a single configuration value. If the config file does not exist, this
command creates it first.

```sh
mast config set node_id pixel-proxy
mast config set advertise_host 100.64.0.10
mast config set adb_port 5038
mast config set programs_dir /home/user/.mast/programs
mast config set device_blacklist android-serial,ios-udid
mast config set android_enabled true
mast config set ios_enabled true
mast config set proxy_enabled true
mast config set lock_portrait true
mast config set keep_display_off false
mast config set stream_idle_timeout 300
mast config set runners..py "python3 -u"
```

Supported keys:

```text
node_id
bind_addr
proxy_addr
api_addr
advertise_host
adb_port
programs_dir
device_blacklist
android_enabled
ios_enabled
proxy_enabled
lock_portrait
keep_display_off
stream_idle_timeout
runners.<file_extension>
```

`device_blacklist` is a comma- or whitespace-separated list of Android serials
and iOS UDIDs. It is consulted on every device listing, so a change through
`PUT /api/nodes/{id}/config` or the blacklist endpoints takes effect on the
running node with no restart. `mast config set` writes the same value for a node
that is not running.

`stream_idle_timeout` is how many seconds an Android viewer stream may run with
nobody watching before Mast tears it down; `0` keeps a stream until something
stops it explicitly. Idleness is counted from the last viewer disconnecting, not
from the client remembering to send a stop — a closed laptop or a dropped tunnel
never sends one, and a stream left running holds a virtual display open and the
phone's encoder busy. It is read per sweep, so a change applies without a
restart. iOS MJPEG streams are unaffected.

`keep_display_off` defaults to `true` (including when the key is absent from an
older config). `mast config set` persists the value for the next start; updating
the same key through `PUT /api/nodes/{id}/config` applies it to a running node.
Set it to `false` to stop Mast from re-asserting the Android
physical-panel-off policy. The separate stay-awake policy remains enabled for
continuous automation.

`lock_portrait` keeps a racked Android phone upright. Turning the rotation sensor
off does not hold on its own — `system_server` writes `accelerometer_rotation`
back to `1` by itself, right after a program relaunches a game's launcher
activity — so the node pins the display with `wm fixed-to-user-rotation` and
re-asserts the whole set on the same 30-second loop that re-asserts stay-awake,
for every device it has ready. Nothing here survives a reboot, which is why it is
a standing policy rather than a one-time preparation step.

Pinning the display refuses an app its own landscape, so the policy also sets
`force_resizable_activities`. Without it an unresizable landscape ad is
letterboxed into a band — roughly 1080x481 of content in a 1080x2340 frame, close
button off the bottom — and a solver sits on an ad it cannot close. Forced
resizable, the ad gets the whole portrait window and lays out inside it.

`PUT /api/devices/{serial}/orientation` still turns one handset deliberately, and
the policy loop re-asserts the operator's rotation rather than overwriting it.
That override is per device and is dropped when the device disconnects, so a
phone that left and came back returns to the node's policy.

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

Resolved device identities are written to `device-identity.json` beside
`config.json`, mapping each wireless adb transport to the hardware serial
behind it. The node maintains this file itself; it exists so a phone that is
unreachable when Mast starts is still identified rather than dropped. Deleting
it is safe — entries are rebuilt the next time each device is reachable.

## peer remove

Removes a peer from `peers.json` and asks the running local Mast node to
disconnect it.

```sh
mast peer remove 100.64.0.20
mast peer remove 100.64.0.20:6270
mast peer remove ws://100.64.0.20:6270/ws
```

Use `--api` if the local Mast API is not listening at the configured
`api_addr`:

```sh
mast peer remove 100.64.0.20 --api http://127.0.0.1:6271
```

## peer ls

Lists peers saved in `peers.json`.

```sh
mast peer ls
```

## device blacklist

Manages the blacklist in `config.json`. Blacklisted Android serials and iOS
UDIDs are omitted from Mast's device list, so normal stream, control,
screenshot, DNS, and program-run paths cannot connect to them.

The list governs Mast only; the adb server keeps whatever transports it has, so
a blacklisted phone stays reachable to `adb` by hand on that host. Excluding a
phone from a node is therefore not the same as cutting the route to it.

Where two nodes can reach one phone, the node holding it locally owns it, so
blacklisting it on the node with the wireless route hands the phone back to the
node holding its cable.

```sh
mast device blacklist add android-serial
mast device blacklist add ios-udid
mast device blacklist ls
mast device blacklist remove ios-udid
mast device blacklist clear
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

Installs Mast as an OS user service. The command copies the current executable
to Mast's stable service binary path, then configures the service to run that
installed binary:

```text
~/.mast/bin/mast
```

On Windows, the installed binary is:

```text
~/.mast/bin/mast.exe
```

```sh
mast service install
```

Platform behavior:

- macOS: writes `~/Library/LaunchAgents/com.brijorn.mast.plist` and reloads it with `launchctl`.
- Linux: writes `~/.config/systemd/user/mast.service`, reloads systemd, enables `mast.service`, and restarts it.
- Windows: writes a scheduled task XML file under the user's Startup programs directory, recreates the `mast` scheduled task, and runs it.

Stop any manually started `mast start` process before installing the service, or
the service may fail to bind its configured ports.

The installed service runs with a PATH that starts with `~/.mast/bin`,
`~/.local/bin`, and `~/bin`. This lets the service and program runs resolve the
installed `mast` binary and user helper commands such as `winerun`.

## service restart

Restarts the installed service.

```sh
mast service restart
```

For local development on Linux and macOS, rebuild Mast into a temporary file
inside the stable service binary directory, then atomically replace the service
binary and restart:

```sh
go build -o ~/.mast/bin/.mast-next ./cmd/mast
mv ~/.mast/bin/.mast-next ~/.mast/bin/mast
mast service restart
```

On Windows, stop the scheduled task first, replace `~/.mast/bin/mast.exe`, then
run the service again:

```powershell
mast service stop
go build -o $env:USERPROFILE\.mast\bin\mast.exe ./cmd/mast
mast service restart
```

GitHub Release updates remain the normal path for updating peer nodes during
development.

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
