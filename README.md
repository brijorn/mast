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
- [CLI](docs/cli.md)
- [Peer websocket protocol](docs/protocol.md)

## Control API

The local control API exposes endpoints for devices, streams, and Android input
commands.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/devices` | List visible Android devices |
| `GET` | `/api/nodes` | List local and connected Mast nodes |
| `POST` | `/api/peers` | Connect to a peer Mast node |
| `POST` | `/api/streams` | Start a scrcpy stream |
| `POST` | `/api/control/tap` | Tap stream coordinates |
| `POST` | `/api/control/swipe` | Swipe stream coordinates |

See [docs/api.md](docs/api.md) for request and response bodies.

See [docs/cli.md](docs/cli.md) for setup, service, peer, and version commands.
