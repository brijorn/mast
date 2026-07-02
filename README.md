# Mast

Mast is a mobile device control layer for coordinating Android and iOS devices across
multiple computers.

##### Current Capabilities:
- Discover local Android devices through ADB, discover local iOS devices through ioslink, and merge peer devices into one node view.
- Connect Mast nodes over the local network or Tailscale with a websocket peer protocol.
- Expose a local control API for devices, nodes, screenshots, scrcpy streams, touch input, keypresses, clipboard access, program runs, config, and updates.
- Start scrcpy streams, including peer-owned streams, stop streams tracked by a node, and replay video websocket packets for late viewers.
- Upload, version, start, resume, stop, clean up, and autostart program bundles.
- Apply selected config changes at runtime and report when a restart is required.
- Optionally expose a local proxy server on a configured port.

The project runs as a lightweight program on each machine that owns devices,
while a main node or dashboard can coordinate each node from one place. It is
intended for trusted private networks.

## Documentation

- [Control API](docs/api.md)
- [CLI](docs/cli.md)
- [Programs](docs/programs.md)
- [Peer websocket protocol](docs/protocol.md)

## Control API

The local control API exposes endpoints for device inventory, stream lifecycle,
input, program runs, node config, and updates.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/devices` | List visible devices |
| `GET` | `/api/devices/{serial}/screenshot` | Capture a device screenshot |
| `GET` | `/api/devices/{serial}/dns` | Read Android private DNS mode |
| `POST` | `/api/devices/{serial}/dns/toggle` | Toggle private DNS between off and AdGuard |
| `GET` | `/api/nodes` | List local and connected Mast nodes |
| `GET` | `/api/nodes/{id}/config` | Read local or peer node config |
| `PUT` | `/api/nodes/{id}/config` | Update local or peer node config |
| `POST` | `/api/peers` | Connect to a peer Mast node |
| `POST` | `/api/streams` | Start a scrcpy stream |
| `DELETE` | `/api/streams/{serial}` | Stop a local or peer-owned scrcpy stream |
| `GET` | `/api/streams/video?serial=...` | Subscribe to stream video packets |
| `POST` | `/api/control/tap` | Tap stream coordinates |
| `POST` | `/api/control/touch` | Send one live touch event |
| `POST` | `/api/control/swipe` | Swipe stream coordinates |
| `POST` | `/api/control/keypress` | Send an Android keycode |
| `POST` | `/api/control/text` | Type text into the focused field |
| `POST` | `/api/control/clipboard/get` | Read device clipboard text |
| `POST` | `/api/control/clipboard/set` | Set device clipboard text |
| `GET` | `/api/programs` | List uploaded programs |
| `POST` | `/api/programs/upload` | Upload a program bundle |
| `GET` | `/api/runs` | List program runs |
| `POST` | `/api/runs` | Start program runs |

See [docs/api.md](docs/api.md) for request and response bodies.

See [docs/programs.md](docs/programs.md) for program storage, run lifecycle,
logs, autostart, cleanup, and template variables.

See [docs/cli.md](docs/cli.md) for setup, service, peer, update, and version
commands.
