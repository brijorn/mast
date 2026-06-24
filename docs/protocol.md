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
    "android_enabled": true
  }
}
```

`android_enabled` tells the peer whether this node should be queried for Android
devices.

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

## Adding Peers

Adding a peer is currently implemented in the node layer with:

```text
node.Connect("ws://host:6270/ws")
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
