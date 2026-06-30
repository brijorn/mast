# Control API

The local control API exposes HTTP endpoints for node operations. It is intended
for local dashboards, CLIs, or trusted tools running on the user's private
network.

## Endpoint Index

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/devices` | List local and peer Android devices. |
| `GET` | `/api/devices/{serial}/screenshot` | Capture a PNG screenshot from a device. |
| `GET` | `/api/devices/{serial}/dns` | Read Android private DNS mode for a device. |
| `POST` | `/api/devices/{serial}/dns/toggle` | Toggle Android private DNS between automatic and AdGuard. |
| `GET` | `/api/nodes` | List the local node and connected peers. |
| `GET` | `/api/nodes/{id}/config` | Read local or peer node config. |
| `PUT` | `/api/nodes/{id}/config` | Update local or peer node config. |
| `GET` | `/api/update` | Check the local node for a Mast release update. |
| `POST` | `/api/update` | Apply a Mast release update on the local node. |
| `GET` | `/api/nodes/{id}/update` | Check a local or peer node for a Mast release update. |
| `POST` | `/api/nodes/{id}/update` | Apply a Mast release update on a local or peer node. |
| `POST` | `/api/peers` | Connect to a peer Mast node. |
| `POST` | `/api/streams` | Start or reuse a scrcpy stream. |
| `DELETE` | `/api/streams/{serial}` | Stop a local or peer-owned scrcpy stream. |
| `GET` | `/api/streams/video?serial=...` | Subscribe to binary video packets over websocket. |
| `GET` | `/api/control/ws?serial=...` | Open a live touch-control websocket. |
| `POST` | `/api/control/touch` | Send one live touch event. |
| `POST` | `/api/control/tap` | Send a tap. |
| `POST` | `/api/control/swipe` | Send a swipe. |
| `POST` | `/api/control/keypress` | Send an Android keycode. |
| `POST` | `/api/control/clipboard/get` | Read clipboard text. |
| `POST` | `/api/control/clipboard/set` | Set clipboard text. |
| `GET` | `/api/programs` | List uploaded programs. |
| `POST` | `/api/programs/upload` | Upload a program bundle. |
| `PUT` | `/api/programs/{id}` | Update program metadata and config mappings. |
| `DELETE` | `/api/programs/{id}` | Delete a current program bundle. |
| `GET` | `/api/runs` | List program runs. |
| `POST` | `/api/runs` | Start program runs. |
| `POST` | `/api/runs/{id}/stop` | Stop a run. |
| `POST` | `/api/runs/{id}/resume` | Resume a completed, stopped, failed, or lost run. |
| `PUT` | `/api/runs/{id}/autostart` | Set run autostart. |
| `GET` | `/api/runs/{id}/logs` | Read run logs, optionally by byte offset. |
| `POST` | `/api/runs/{id}/cleanup` | Remove a completed run workspace. |

## List Devices

```http
GET /api/devices
```

Returns Android devices visible to the local node and Android-enabled peers.

```json
[
  {
    "serial": "local-123",
    "state": "device",
    "battery_percent": 81,
    "power_connected": true,
    "power_source": "usb",
    "battery_status": "charging",
    "power_health": "charging",
    "battery_current_now": 432000,
    "battery_current_avg": 380000,
    "battery_trend_percent_per_hour": 6.4,
    "node_id": "node-a"
  }
]
```

Battery and power fields are omitted when Android does not expose that detail or
the device is not in a usable `device` state.

## Capture Screenshot

```http
GET /api/devices/{serial}/screenshot
```

Returns a PNG screenshot for a local or peer-owned device.

Successful response:

```http
200 OK
Content-Type: image/png
Cache-Control: no-store
```

## Device DNS

```http
GET /api/devices/{serial}/dns
```

Returns Android private DNS state for a local or peer-owned device.

```json
{
  "mode": "opportunistic",
  "automatic": true
}
```

```http
POST /api/devices/{serial}/dns/toggle
```

When the device is automatic, Mast sets private DNS to `dns.adguard.com`. When
the device is not automatic, Mast returns it to automatic mode. The response is
the device DNS state after the change.

```json
{
  "mode": "hostname",
  "hostname": "dns.adguard.com",
  "automatic": false
}
```

## List Nodes

```http
GET /api/nodes
```

Returns the local Mast node and all connected peer nodes known to it.

```json
[
  {
    "id": "node-a",
    "local": true,
    "android_enabled": true,
    "ios_enabled": false,
    "proxy_enabled": true,
    "adb_port": 5037,
    "version": "0.1.0",
    "commit": "abc123",
    "build_date": "2026-06-25T17:00:00Z"
  },
  {
    "id": "node-b",
    "addr": "100.64.0.2",
    "local": false,
    "android_enabled": false,
    "ios_enabled": false,
    "proxy_enabled": false,
    "adb_port": 5038,
    "version": "0.1.0",
    "commit": "def456",
    "build_date": "2026-06-25T17:00:00Z"
  }
]
```

## Get Node Config

```http
GET /api/nodes/{id}/config
```

Returns the selected local or peer node's persisted runtime config.

```json
{
  "node_id": "pixel-proxy",
  "bind_addr": ":6270",
  "proxy_addr": ":6272",
  "api_addr": ":6271",
  "advertise_host": "127.0.0.1",
  "adb_port": 5037,
  "programs_dir": "/home/user/.mast/programs",
  "android_enabled": true,
  "ios_enabled": false,
  "proxy_enabled": false,
  "lock_portrait": false,
  "runners": {
    ".py": "python3"
  }
}
```

## Update Node Config

```http
PUT /api/nodes/{id}/config
```

Updates the selected local or peer node's config, applies fields that can change
during the current run, and saves `config.json` for future runs. The request may
wrap values in `values` or send config keys directly. Runner entries can be sent
as a `runners` object or as `runners.<extension>` keys.

```json
{
  "values": {
    "node_id": "pixel-proxy",
    "android_enabled": true,
    "ios_enabled": false,
    "proxy_enabled": true,
    "lock_portrait": true,
    "adb_port": 5038,
    "api_addr": ":7001",
    "runners": {
      ".py": "python3"
    }
  }
}
```

Response body:

```json
{
  "config": {
    "node_id": "pixel-proxy",
    "bind_addr": ":6270",
    "proxy_addr": ":6272",
    "api_addr": ":7001",
    "advertise_host": "127.0.0.1",
    "adb_port": 5038,
    "programs_dir": "/home/user/.mast/programs",
    "android_enabled": true,
    "ios_enabled": false,
    "proxy_enabled": true,
    "lock_portrait": true,
    "runners": {
      ".py": "python3"
    }
  },
  "changed_keys": ["adb_port", "android_enabled", "api_addr", "lock_portrait", "node_id", "proxy_enabled", "runners..py"],
  "restart_required": true,
  "restart_required_keys": ["api_addr", "node_id"]
}
```

Supported config keys are `node_id`, `bind_addr`, `proxy_addr`, `api_addr`,
`advertise_host`, `adb_port`, `programs_dir`, `android_enabled`, `ios_enabled`,
`proxy_enabled`, `lock_portrait`, and `runners.<file_extension>`.

Listener and directory fields such as `bind_addr`, `api_addr`, `proxy_addr`, and
`programs_dir` are persisted immediately but require a restart to fully take
effect. Changing `node_id` also requires a restart because it changes the
peer identity advertised by the running node.

Runtime fields such as Android/iOS visibility, ADB port, advertised host, proxy
enablement, portrait locking, and runner mappings are applied to the running
node when possible. Changing `proxy_addr` while the proxy is already running
still requires a restart.

## Add Peer

```http
POST /api/peers
```

Connects the running Mast node to another Mast peer.

Request body:

```json
{
  "target": "100.64.0.20:6270"
}
```

`target` may be a host, `host:port`, or full websocket URL. If the port is
omitted, Mast uses `6270`; if the path is omitted, Mast uses `/ws`.

Response body:

```json
{
  "url": "ws://100.64.0.20:6270/ws"
}
```

## Check Local Update

```http
GET /api/update
```

Checks whether the local Mast binary has an available GitHub Release update.
The response body matches `GET /api/nodes/{id}/update`.

## Apply Local Update

```http
POST /api/update
```

Applies an update to the local Mast binary. If `restart` is true and the update
succeeds, Mast flushes the JSON response before scheduling its own restart.

Request body:

```json
{
  "force": false,
  "restart": false
}
```

The response body matches `POST /api/nodes/{id}/update`.

## Check Node Update

```http
GET /api/nodes/{id}/update
```

Checks whether the selected local or peer Mast node has an update available.

```json
{
  "current_version": "0.1.0",
  "latest_version": "0.2.0",
  "update_available": true,
  "os": "darwin",
  "arch": "arm64",
  "asset_name": "mast_0.2.0_darwin_arm64.tar.gz",
  "asset_url": "https://github.com/brijorn/mast/releases/download/v0.2.0/mast_0.2.0_darwin_arm64.tar.gz",
  "checksum_url": "https://github.com/brijorn/mast/releases/download/v0.2.0/checksums.txt"
}
```

## Apply Node Update

```http
POST /api/nodes/{id}/update
```

Asks the selected local or peer Mast node to update itself. The peer downloads,
verifies, extracts, and replaces its own binary.

Request body:

```json
{
  "force": false,
  "restart": false
}
```

Response body:

```json
{
  "current_version": "0.1.0",
  "latest_version": "0.2.0",
  "updated": true,
  "restart_required": true,
  "message": "updated to 0.2.0; restart required"
}
```

## Start Stream

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
    "turn_screen_off": true,
    "stay_awake": true,
    "max_size": 1080,
    "video_bitrate": 8000000,
    "video_codec_options": "i-frame-interval=1"
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

## Stop Stream

```http
DELETE /api/streams/{serial}
```

Stops a local or peer-owned stream for the serial. If the serial belongs to a
peer device, Mast routes the stop request to the owner node.

Successful response:

```http
204 No Content
```

## Stream Video WebSocket

```http
GET /api/streams/video?serial=local-123
```

Subscribes to an already-started stream over websocket. Messages are binary
video packets:

```text
byte 0      flags: bit 0 = codec config, bit 1 = keyframe
bytes 1-8   big-endian presentation timestamp
bytes 9-12  big-endian payload size
bytes 13+   H.264 payload
```

New subscribers receive the latest cached codec config and a recent keyframe
when available, then live packets.

## Tap

```http
POST /api/control/tap
```

Sends a tap command to a device. The stream for that serial must already be
started so Mast has a scrcpy control socket.

Request body:

```json
{
  "serial": "local-123",
  "x": 320,
  "y": 640
}
```

Successful response:

```http
204 No Content
```

## Touch

```http
POST /api/control/touch
```

Sends one live touch event to a device. Use this for drag gestures where the
client sends `down`, one or more `move` events, then `up`.

Request body:

```json
{
  "serial": "local-123",
  "action": "move",
  "x": 320,
  "y": 640
}
```

`action` must be `down`, `move`, or `up`.

Successful response:

```http
204 No Content
```

## Swipe

```http
POST /api/control/swipe
```

Sends a swipe command to a device. The stream for that serial must already be
started so Mast has a scrcpy control socket.

Request body:

```json
{
  "serial": "local-123",
  "start_x": 320,
  "start_y": 900,
  "end_x": 320,
  "end_y": 200
}
```

Successful response:

```http
204 No Content
```

## Press Key

```http
POST /api/control/keypress
```

Sends an Android keycode through the active scrcpy control socket.

Request body:

```json
{
  "serial": "local-123",
  "keycode": 3,
  "meta_state": 0
}
```

`keycode` must be a known scrcpy Android keycode. `meta_state` is optional.

Successful response:

```http
204 No Content
```

## Get Clipboard

```http
POST /api/control/clipboard/get
```

Reads clipboard text through the active scrcpy control socket.

Request body:

```json
{
  "serial": "local-123"
}
```

Response body:

```json
{
  "text": "clipboard text"
}
```

## Set Clipboard

```http
POST /api/control/clipboard/set
```

Sets clipboard text through the active scrcpy control socket.

Request body:

```json
{
  "serial": "local-123",
  "text": "new clipboard text"
}
```

Successful response:

```http
204 No Content
```

## Live Control WebSocket

```http
GET /api/control/ws?serial=local-123
```

Opens a low-latency control channel for live interaction. The stream for that
serial must already be started so Mast has a scrcpy control socket. Mast sends
websocket ping frames to keep the connection alive, validates each JSON message,
and applies accepted control messages in receive order.

Touch message:

```json
{
  "type": "touch",
  "action": "move",
  "x": 320,
  "y": 640
}
```

Swipe message:

```json
{
  "type": "swipe",
  "start_x": 320,
  "start_y": 900,
  "end_x": 320,
  "end_y": 200
}
```

Error message:

```json
{
  "type": "error",
  "message": "action must be down, move, or up"
}
```

## Program APIs

Program storage, versioning, resume behavior, logs, cleanup, autostart, and
template variables are covered in [Programs](programs.md). The HTTP surface is:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/programs` | List current uploaded programs. |
| `POST` | `/api/programs/upload` | Upload a multipart program bundle. |
| `PUT` | `/api/programs/{id}` | Update `name`, `slug`, and `config_mappings`. |
| `DELETE` | `/api/programs/{id}` | Delete a program by content ID or slug. |
| `GET` | `/api/runs` | List known runs. |
| `POST` | `/api/runs` | Start the current program by slug or content ID. |
| `POST` | `/api/runs/{id}/stop` | Stop a run and disable its autostart flag. |
| `POST` | `/api/runs/{id}/resume` | Resume the same run ID and workspace. |
| `PUT` | `/api/runs/{id}/autostart` | Enable or disable autostart for a run. |
| `GET` | `/api/runs/{id}/logs` | Read stdout/stderr with optional offsets. |
| `POST` | `/api/runs/{id}/cleanup` | Delete a completed run workspace. |

Start run request:

```json
{
  "program_id": "my-script",
  "serials": ["local-123"],
  "variables": {
    "MAX_LEVELS": "30"
  }
}
```

Resume run request:

```json
{
  "variables": {
    "MAX_LEVELS": "30"
  }
}
```

Autostart request:

```json
{
  "enabled": true
}
```

## Coordinate Space and Routing

Tap, touch, and swipe coordinates are scrcpy stream coordinates. Mast reads the
stream width and height from scrcpy metadata when the stream starts, then uses
those dimensions when writing control messages.

If a device command targets a serial owned by another node, the receiving API
node routes the command over the peer websocket to the device owner.
