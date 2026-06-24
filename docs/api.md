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

Tap and swipe coordinates are scrcpy stream coordinates. Mast reads the stream
width and height from scrcpy metadata when the stream starts, then uses those
dimensions when writing control messages.

If a command targets a device owned by another node, the receiving API node
routes the command over the peer websocket to the device owner.
