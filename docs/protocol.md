# Peer Websocket Protocol

Mast nodes listen for peer connections at:

```http
GET /ws
```

Heartbeat traffic is handled with websocket ping/pong frames, not JSON protocol
messages.

Every JSON protocol message shares these fields:

```typescript
type: string
id: string
from: string
to: string
timestamp: string
payload: object
```

`connection_request` may leave `to` empty because the peer's node ID is not
known until the handshake completes. Directed command messages should set `to`
to the destination node ID.

## connection_request

Sent when a node introduces itself to a peer.

```json
{
  "type": "connection_request",
  "id": "message-id",
  "from": "node-a",
  "to": "",
  "timestamp": "2026-06-22T17:00:00Z",
  "payload": {
    "android_enabled": true,
    "ios_enabled": false,
    "proxy_enabled": true,
    "version": "0.1.0",
    "commit": "abc123",
    "build_date": "2026-06-25T17:00:00Z"
  }
}
```

`android_enabled` tells the peer whether this node should be queried for Android
devices. `ios_enabled` is reserved for iOS device support. `proxy_enabled`
tells the peer whether this node has its proxy server enabled.
The version fields describe the Mast binary running on that node.

## start_stream_request

Requests that the device owner start a scrcpy stream.

```json
{
  "type": "start_stream_request",
  "id": "message-id",
  "from": "node-a",
  "to": "node-b",
  "timestamp": "2026-06-22T17:00:00Z",
  "payload": {
    "serial": "remote-123",
    "options": {
      "no_audio": true,
      "no_control": false,
      "turn_screen_off": false,
      "stay_awake": true,
      "max_size": 1080,
      "video_bitrate": 8000000
    }
  }
}
```

## stop_stream_request

Requests that the device owner stop a scrcpy stream.

```json
{
  "type": "stop_stream_request",
  "id": "message-id",
  "from": "node-a",
  "to": "node-b",
  "timestamp": "2026-06-22T17:00:00Z",
  "payload": {
    "serial": "remote-123"
  }
}
```

## tap_request

Requests that the device owner send a tap through the active scrcpy control
socket.

```json
{
  "type": "tap_request",
  "id": "message-id",
  "from": "node-a",
  "to": "node-b",
  "timestamp": "2026-06-22T17:00:00Z",
  "payload": {
    "serial": "remote-123",
    "x": 320,
    "y": 640
  }
}
```

## touch_request

Requests that the device owner send one live touch event through the active
scrcpy control socket. A drag gesture should send `down`, one or more `move`
messages, then `up`.

```json
{
  "type": "touch_request",
  "id": "message-id",
  "from": "node-a",
  "to": "node-b",
  "timestamp": "2026-06-22T17:00:00Z",
  "payload": {
    "serial": "remote-123",
    "action": "move",
    "x": 320,
    "y": 640
  }
}
```

`action` must be `down`, `move`, or `up`.

## swipe_request

Requests that the device owner send a swipe through the active scrcpy control
socket.

```json
{
  "type": "swipe_request",
  "id": "message-id",
  "from": "node-a",
  "to": "node-b",
  "timestamp": "2026-06-22T17:00:00Z",
  "payload": {
    "serial": "remote-123",
    "start_x": 320,
    "start_y": 900,
    "end_x": 320,
    "end_y": 200
  }
}
```

## update_check_request

Requests that the destination node check its own latest available GitHub
release.

```json
{
  "type": "update_check_request",
  "id": "message-id",
  "from": "node-a",
  "to": "node-b",
  "timestamp": "2026-06-25T17:00:00Z",
  "payload": null
}
```

The response uses the same message ID:

```json
{
  "type": "update_check_response",
  "id": "message-id",
  "from": "node-b",
  "to": "node-a",
  "timestamp": "2026-06-25T17:00:01Z",
  "payload": {
    "result": {
      "current_version": "0.1.0",
      "latest_version": "0.2.0",
      "update_available": true,
      "os": "darwin",
      "arch": "arm64"
    }
  }
}
```

If the check fails, `payload.error` contains the error string.

## update_apply_request

Requests that the destination node apply an update to itself.

```json
{
  "type": "update_apply_request",
  "id": "message-id",
  "from": "node-a",
  "to": "node-b",
  "timestamp": "2026-06-25T17:00:00Z",
  "payload": {
    "force": false,
    "restart": false
  }
}
```

The response uses the same message ID:

```json
{
  "type": "update_apply_response",
  "id": "message-id",
  "from": "node-b",
  "to": "node-a",
  "timestamp": "2026-06-25T17:00:02Z",
  "payload": {
    "result": {
      "current_version": "0.1.0",
      "latest_version": "0.2.0",
      "updated": true,
      "restart_required": true,
      "message": "updated to 0.2.0; restart required"
    }
  }
}
```

If the update fails, `payload.error` contains the error string.

## Adding Peers

Adding a peer can be done from the CLI:

```sh
mast peer add 100.64.0.20
```

or from the HTTP API:

```http
POST /api/peers
```

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
