# Mast

An Android/IOS device control layer for coordinating devices
across multiple computers.

##### Current Capabilities:
- Discover Android devices through ADB (Peer devices are also shown)
- Connect to peer nodes over the local network
- Expose a local control API for operations such as device listing and scrcpy stream startup.
- Expose a proxy server on a port

The project runs as a lightweight program on each machine that owns devices, while a main node or dashboard can coordinate each node from one place. Intended for use in a private network or with Tailscale

## Peer Websocket Protocol

Mast nodes listen for peer connections at:

```http
GET /ws
```

The websocket currently defines one JSON message type:

- `connection_request`

Heartbeat traffic is handled with websocket ping/pong frames, not JSON protocol
messages.

Every JSON protocol message shares these fields:

```typescript
type: string // Message type, currently "connection_request"
id: string // Unique message UUID
from: string // Sender node ID
to: string // Destination node ID; currently unused for connection_request
timestamp: string // JSON-encoded timestamp
payload: object // Message-specific payload
```

### connection_request

Sent when a node introduces itself to a peer.

```json
{
  "type": "connection_request",
  "id": "message-id",
  "from": "node-a",
  "to": "",
  "timestamp": "2026-06-22T17:00:00Z",
  "payload": {
    "android_enabled": true
  }
}
```

`android_enabled` tells the peer whether this node should be queried for Android
devices.

## Adding Peers

Adding a peer is currently implemented in the node layer with:

```text
node.Connect("ws://host:8080/ws")
```

There is not yet a CLI command or HTTP endpoint for adding peers.

The connection flow is:

1. The receiving node runs its websocket listener on `/ws`.
2. The initiating node dials the receiver's websocket URL.
3. The initiating node immediately sends `connection_request`.
4. The receiver stores the peer under the sender's `from` node ID.
5. The receiver replies with its own `connection_request`.
6. The initiator stores the receiver under the receiver's `from` node ID.
7. Both sides keep the connection alive with websocket ping/pong frames.

If an initiated connection drops, the initiator attempts to reconnect with
exponential backoff up to 60 seconds.

## Control API

The local control API exposes HTTP endpoints for node operations.

### List Devices

```http
GET /api/devices
```

Returns Android devices visible to the local node and Android-enabled peers.

```json
[
  {
    "serial": "local-123",
    "state": "device",
    "node_id": "node-a"
  }
]
```

### Start Stream

```http
POST /api/streams
```

Starts a scrcpy stream for a device serial. Only one stream start is allowed per
serial at a time; concurrent requests for the same serial wait for the same
startup result.

Request body:

```json
{
  "serial": "local-123",
  "options": {
    "no_audio": true,
    "no_control": false,
    "turn_screen_off": false,
    "stay_awake": true,
    "max_size": 1080,
    "video_bitrate": 8000000
  }
}
```

Response body:

```json
{
  "id": "stream-session-id",
  "serial": "local-123",
  "host": "100.64.0.10",
  "local_port": 12345
}
```

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
