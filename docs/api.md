# Control API

The local control API exposes HTTP endpoints for node operations. It is intended
for local dashboards, CLIs, or trusted tools running on the user's private
network.

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
    "node_id": "node-a"
  }
]
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
    "version": "0.1.0",
    "commit": "abc123",
    "build_date": "2026-06-25T17:00:00Z"
  },
  {
    "id": "node-b",
    "addr": "100.64.0.2",
    "local": false,
    "android_enabled": false,
    "version": "0.1.0",
    "commit": "def456",
    "build_date": "2026-06-25T17:00:00Z"
  }
]
```

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

## Coordinate Space

Tap, touch, and swipe coordinates are scrcpy stream coordinates. Mast reads the
stream width and height from scrcpy metadata when the stream starts, then uses
those dimensions when writing control messages.

If a command targets a device owned by another node, the receiving API node
routes the command over the peer websocket to the device owner.
