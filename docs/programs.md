# Programs

This document describes how Mast stores, versions, and cleans up program
bundles and run instances.

## Concepts

| Term | Description |
|---|---|
| **Bundle** | A snapshot of a program's files, stored content-addressed under `bundles/`. |
| **Slug** | A stable URL-safe identifier derived from the program name (e.g. `"My App"` → `"my-app"`). |
| **Instance** | A per-run copy of a bundle in `instances/<run-id>/`. Runs execute inside the instance. |

## Directory layout

```
~/.mast/programs/
  registry.json           – current program metadata, one entry per slug
  bundles/
    sha256-<hash>/        – one directory per registered bundle
      mast-program.json   – program metadata
      ...                 – program files
  instances/
    <run-uuid>/           – one directory per run
      run.json            – run metadata
      stdout.log
      stderr.log
      ...                 – copy of the bundle files at start time
```

## Registering a program

### Via API (multipart)

```http
POST /api/programs/upload
Content-Type: multipart/form-data
```

| Field | Type | Description |
|---|---|---|
| `name` | string | Human-readable program name (optional; defaults to `"unnamed"`) |
| `slug` | string | Stable program slug (optional; derived from `name` when omitted) |
| `entry` | JSON string | Main command plus optional managed companions (below). |
| `config_file` | string | Path to the config file inside the bundle (optional) |
| `config_mappings` | JSON string | `[{"section":"Settings","key":"DEVICE_ID","value":"{{phone.serial}}"}]` |
| `finishes_on_clean_exit` | `"true"` | A zero exit means this program finished the work it was given, so a crash restart leaves it alone (optional; defaults to off) |
| `files` | file (multiple) | Each file part's filename is its relative path in the bundle |

Response: `201 Created` with the `Program` JSON object.

### Managed companions

An entry can declare helper processes that share the main run's workspace,
environment, logs, process group, Stop action, and resume snapshot:

```json
{
  "command": "main.exe",
  "companions": [
    {
      "id": "new-game-helper",
      "command": "bin/new-game-helper",
      "args": ["run"],
      "enabled_when": { "variable": "ALT_LAYOUT", "equals": "true" },
      "required": true
    }
  ]
}
```

The condition is evaluated only for a new run. The resolved companion command
is persisted in `run.json`, so Resume reproduces the same process set. A
required companion that cannot start or exits unexpectedly fails and stops the
complete run. An optional companion start failure is recorded on that companion
without preventing the main process from running. Main-process exit and
explicit Stop terminate all companions.

### Updating program metadata

```http
PUT /api/programs/{id}
```

Updates the current program record's `name`, `slug`, and `config_mappings`
without changing the stored bundle files:

```json
{
  "name": "My App",
  "slug": "my-app",
  "config_mappings": [
    {
      "section": "Settings",
      "key": "DEVICE_ID",
      "value": "{{phone.serial}}",
      "comment": "Device serial used by the script"
    }
  ]
}
```

### Deleting a program

```http
DELETE /api/programs/{id}
```

Deletes the current program record and its bundle directory. The path value may
be the content-hash ID or the slug. Existing run instances keep their copied
workspace.

## Versioning

Mast uses a **latest-only** versioning model:

- Each program has a **slug** derived from its name.
- `registry.json` stores one current program entry per slug.
- Re-uploading the same slug replaces the current bundle entry and
  increments that program's `version`.
- Replacing a bundle deletes the previous bundle directory. Existing runs keep
  their own copied instance workspace.

### Starting a program by slug

`POST /api/runs` accepts either a content-hash ID **or** a slug in the
`program_id` field. When a slug is given, Mast resolves it to the current
bundle automatically:

```json
{"program_id": "my-app", "serials": ["device-123"]}
```

This always starts the latest uploaded version of `my-app`.

### Program versions

`GET /api/programs` includes the current `version` for each program. Each run
stores the `program_slug`, `program_version`, and content-hash `program_id`
that it started with. Clients can compare run metadata with the current program
metadata when they need to show update state.

Running instances are never interrupted by an upload — they hold their own
copy of the bundle files in their instance workspace.

## Standard run environment

Mast injects the device-routing contract into every new run:

| Variable | Meaning |
|---|---|
| `MAST_API_URL` | Mast API used by deployed device clients. |
| `DEVICE_SERIAL` | adb transport for the selected device. Pass this to `adb -s`. |
| `DEVICE_ADDRESS` | Explicit alias for `DEVICE_SERIAL`. |
| `DEVICE_ID` | The device's durable identity: hardware serial or iOS UDID. |
| `DEVICE_PLATFORM` | `android` or `ios`. |
| `MAST_NODE_ID` | Node that owns the selected device. |
| `ANDROID_SERIAL` | Android-only compatibility alias for `DEVICE_SERIAL`. |

`DEVICE_SERIAL` and `DEVICE_ID` differ only for a wirelessly connected device.
adb knows such a device only as `host:port`, so every handle a program hands to
adb carries that address; `adb -s` with a hardware serial fails with "device
not found". `DEVICE_ID` is the value Mast and Runway key durable state on, and
is what a program should record in evidence or report upward — it survives the
reconnect that changes the address.

Programs are expected to use these values to route screenshots and controls
through Mast rather than starting their own ADB, WDA, tunnel, or ioslink
sessions in a deployed workspace.

## Android automation power policy

Mast owns the power policy for every ready local Android device. It sets
Android's `stay_on_while_plugged_in` value to `7` so the device remains awake on
AC, USB, or wireless power independently of any video viewer.

By default, Mast also keeps the physical panel powered off. Each device gets a
lightweight scrcpy session with video and audio disabled and a dedicated
control socket, separate from the viewer stream socket. The session starts with
`power_on=false`, sends `SET_DISPLAY_POWER(false)` immediately and every 30
seconds, and uses `cleanup=false`. Disabling cleanup is intentional: scrcpy's
`power_off_on_close` path injects a POWER key and can put the Android device to
sleep, while disabling cleanup simply prevents scrcpy from restoring display
power. Policy-protected viewer sessions use the same `power_on=false` and
`cleanup=false` safeguards. Consequently, releasing or replacing a viewer
stream cannot restore the panel or sleep the automation device. Mast recreates
the policy session after its control socket ends and after the existing
device-readiness monitor observes a reconnect; the same readiness observation
establishes it after Mast starts.

This changes only physical display power. ADB screenshots and injected input
continue to use the awake Android device and work while the panel is dark.
Viewer startup allows two seconds for the first video keyframe before checking
Android's power-manager interactive state. A confirmed non-interactive device
is woken only when the display-off policy is not active. An interactive device
is simply given the remaining startup budget, since encoder or network delay is
not evidence that it is asleep. When `keep_display_off` is active, Mast never
wakes the device from this path and re-asserts the panel-off policy instead.
Failure to query power state or send a recovery command is logged and does not,
by itself, abort the stream.

To allow deliberate operator control of the physical panel, opt out at runtime
through the node config API:

```http
PUT /api/nodes/{node-id}/config
Content-Type: application/json

{"values":{"keep_display_off":"false"}}
```

The device remains awake for automation, but Mast stops maintaining and
re-asserting the display-off control session. Set the value back to `true` to
restore the default policy. Existing config files that omit
`keep_display_off` are treated as enabled.

---

## Instance cleanup

Instance workspaces can grow large for long-running programs. Mast does not
automatically remove previous workspaces when a new run starts; callers decide
which completed run, if any, should be cleaned up.

### Cleanup via API

To free the workspace of a specific completed run immediately:

```http
POST /api/runs/{id}/cleanup
```

Returns `400 Bad Request` if the run is still active. Lost runs can be cleaned
up only after Mast confirms the saved process is no longer alive. Returns the
updated `Run` object with `"workspace_cleaned": true` on success.

### Resume

`POST /api/runs/{id}/resume` re-runs the saved command in the same instance
workspace and preserves the same run ID. Mast uses this for `exited`, `failed`,
`stopped`, or `lost` runs. When the previous attempt failed, Resume rotates its
current logs before starting; clean exits and explicit stops replace the current
logs without retaining history. By default, resume uses the run's current config
values. To change them, send a JSON body with `variables`; those values are
applied to the process environment and the rendered config file, and become the
run's stored `env` from that point on.

They persist deliberately. A crash-restart supervisor resumes with no variables
of its own, so an override held for one attempt would be reverted by the first
crash, leaving a run executing configuration its operator had already replaced.
A caller wanting the original values back sends them explicitly.

```json
{
  "variables": {
    "MAX_LEVELS": "30"
  }
}
```

If a lost run's saved PID is still alive, Mast verifies ownership by the saved
run workspace where the platform supports it, then terminates that process tree
before starting the replacement. Mast does not compare process argv because
wrappers such as Wine can replace the visible command line after launch.

The set of enabled companions never changes on Resume, even when variable
overrides are supplied.

### Logs

`GET /api/runs/{id}/logs` returns stdout and stderr. Without query parameters,
the response contains the full current log files.

Clients can poll incrementally by passing byte offsets:

```http
GET /api/runs/{id}/logs?stdout_offset=123&stderr_offset=456
```

The response includes appended `stdout` and `stderr` chunks plus
`stdout_offset` and `stderr_offset` values for the next request. If a log file
was truncated, such as after resume, Mast returns the current file from the
beginning and sets the corresponding `*_reset` flag.

Mast caps each current stdout/stderr stream to 10 MiB. When the file exceeds the
cap, Mast keeps the newest bytes and records the logical start offset in
`run.json` so offset polling can continue. If a client asks for an offset older
than the retained window, Mast returns the retained window and sets the
corresponding reset flag.

A failed attempt is retained for up to three resumed generations:
`stdout.1.log` through `stdout.3.log` and `stderr.1.log` through
`stderr.3.log`, where generation 1 is the most recent failed attempt. The logs
API continues to read only the current `stdout.log` and `stderr.log`; the
existing current-log offsets reset to zero for the resumed attempt. Historical
files use the same per-file 10 MiB bound already applied while they were
current, with no additional aggregate size cap.

### Autostart

`PUT /api/runs/{id}/autostart` stores two run-owned automatic recovery flags.
They can be set independently:

```json
{
  "autostart_reconnect": true,
  "autostart_crash_restart": false
}
```

Either field may be omitted to leave that behavior unchanged. The legacy
request remains supported and sets both flags together:

```json
{"enabled": true}
```

`GET /api/runs` and every persisted `run.json` expose both values. They also
retain `autostart` as a compatibility aggregate that is true when either
behavior is enabled. This lets old clients disable both through the legacy
request without hiding that some automatic recovery is still active.

When Mast starts, it automatically resumes runs with `autostart_reconnect`
enabled that are `stopped` or `lost`, using the same run ID and instance
workspace. Startup recovery is grouped with reconnect recovery because both
restore work after Mast or the device went away, rather than reacting to a
program failure while the device stayed connected.

While Mast is running, two independently gated watches resume runs using that
same run ID and workspace:

- **Device reconnect (`autostart_reconnect`).** A run whose device leaves and
  returns is resumed on the ready-state transition.
- **Ended on its own (`autostart_crash_restart`).** A run that reached `failed`
  or `exited` while its device stayed connected produces no such transition, so
  it is resumed on a backoff instead. Without this the phone stays idle until a
  human notices, because the reconnect watch never fires for a program that
  simply exited.

  A program registered with `finishes_on_clean_exit` narrows that watch: a zero
  exit from one of those is the run reporting that it did the work it was
  configured for, so it is left alone and only a failure or a non-zero exit is
  restarted. Restarting a finished run makes its configuration unenforceable —
  a run bounded at twenty levels was resumed every time it reached twenty, and
  because a run keeps its progress across a resume it played one more level per
  attempt and reported twenty-eight of a limit of twenty. A program that ends
  for its own reasons and expects to be started again, such as a licensed
  executable closing after a session, does not declare it and keeps being
  resumed whenever it ends on its own.

The backoff starts at 30 seconds and doubles per consecutive attempt to a
15-minute ceiling. Restart attempts remain in the same incident until the run
has gone six hours without another failure. This failure-rate window means a
run that repeatedly dies after 11 or 12 minutes still escalates, while a stable
run that crashes again weeks later starts with a clean streak. After 8 restart
attempts in one incident, the supervisor leaves the run alone because restarts
are not correcting the failure. Runs that are `autostart_paused` or
workspace-cleaned are never resumed by either watch. Each watch also requires
its own behavior flag, and neither watch acts while the device is not ready.

`GET /api/runs` exposes the durable incident state on the run so clients can
surface pending recovery or give-up without reading the Mast journal:

```json
{
  "autostart_supervisor": {
    "restart_attempts": 2,
    "abandoned": false,
    "last_error": "exit status 1",
    "last_failure_at": "2026-07-26T14:12:00Z",
    "next_restart_at": "2026-07-26T14:14:00Z"
  }
}
```

`next_restart_at` is present only while a failed or exited run has a scheduled
crash-restart attempt. It is cleared before the attempt starts and when the
supervisor gives up, so API clients can distinguish pending recovery from a
terminal failure without guessing Mast's backoff.

This state is checkpointed in `run.json` and survives a Mast restart. An
explicit Resume, disabling `autostart_crash_restart`, or disabling both through
the legacy request clears it; a supervisor-driven Resume preserves it so
subsequent failures continue the same incident. A running attempt clears it
automatically after the six-hour failure-free window.

Run checkpoint schema version 1 contained only `autostart`. On load, Mast
migrates that value into both new fields and rewrites the checkpoint as schema
version 2. Consequently every legacy run with `autostart: true` keeps both
reconnect and crash recovery enabled after upgrading.

Manual `POST /api/runs/{id}/stop` preserves autostart for that run. Clients can
send `{"autostart_paused": true}` with the stop request when a run should stay
stopped until an explicit resume, such as battery protection waiting for a
resume threshold. Mast's own shutdown path stops active programs without
pausing autostart, so configured runs come back when Mast is launched again.

When Mast restarts while a run is active, it restores that run as `lost` rather
than `failed`, because Mast no longer knows whether the program itself failed.

### Cooperative stop

Clients that need graceful cleanup can request, poll, and acknowledge a soft
stop through `/api/runs/{id}/stop-request` and `/api/runs/{id}/stop-ack`.
Requesting or acknowledging does not kill the process; the program exits itself
after cleanup, or the coordinator follows with the normal `/stop` endpoint.
Both operations are idempotent and retain the first request and acknowledgement
timestamps.

Each workspace keeps a schema-versioned, monotonically revisioned `run.json`
recovery checkpoint. Mast writes checkpoints through a same-directory temporary
file, flushes them, and atomically replaces the prior snapshot so a crash cannot
leave partially serialized JSON. A delayed older revision cannot replace a
newer checkpoint.

---

## Replaced bundles

When a program is re-uploaded, the old bundle directory (`bundles/<old-hash>/`)
is deleted after the new bundle is registered. Mast does not use symlinks for
run instances; each run gets a full copy of the bundle files in its instance
workspace. Existing runs do not need the replaced bundle directory to keep
executing.

Runs store the program slug and version in `run.json`, so clients can still
compare a run with the current registry entry after the replaced bundle record
is removed.

---

## Custom Program Runners

When Mast starts a program, it normally runs the entry command directly. If the
entrypoint is a script or non-native executable, the host machine can configure
a wrapper command keyed by file extension.

This configuration is stored in the local host's `~/.mast/config.json`
configuration file, so it is specific to the machine running the program.

### Matching Order
When looking up a runner for a program, Mast evaluates the following:
1. **File extension**: Looks up the entrypoint command's file extension in the `runners` map (e.g., `.py` or `.exe`).

If a non-native executable such as `.exe` is started on a non-Windows host,
Mast requires an explicit runner. Without one, the run fails before the process
is started.

### Runner Formatting
Runner commands can contain flags. When executing, the wrapper is split and any additional arguments are prepended before the target executable/file path.

For example, given:
```sh
mast config set runners..py "python3 -u"
```
If a program with entry command `test.py` and arguments `["arg1"]` is executed, Mast will run:
```sh
python3 -u test.py arg1
```

## Run environment

Mast adds `PYTHONUNBUFFERED=1` to each run by default so Python and PyInstaller
programs flush stdout/stderr promptly when their output is captured in log
files. Run variables can override this value when a program explicitly needs a
different setting.

---

## Configuration Variables & Templates

Programs can contain dynamic placeholders in their configured `config_file` or
program arguments.

### Template Placeholders
Placeholders are defined using `{{placeholder}}` token notation.

#### 1. Built-in Tokens

Built-in tokens are automatically populated by Mast depending on the executing
phone. There are exactly three supported built-in tokens:

- `{{phone.serial}}` - Replaced with the phone's adb transport, so the value can
  be passed straight to `adb -s`. For a wireless phone this is `host:port`.
- `{{phone.id}}` - Replaced with the phone's durable identity (hardware serial
  or iOS UDID). Use this for anything recorded or reported, not for adb.
- `{{phone.node_id}}` - Replaced with the node ID of the host.

#### 2. Custom Tokens

Any other token, such as `{{license_key}}` or `{{resolution}}`, represents a
custom variable. Clients can collect those values before starting or resuming a
run and pass them in the `variables` object.

Sensitive config values use `secret_variables`, never ordinary `variables` or
literal program mappings. Mast uses them while rendering the workspace config,
stores them only in the workspace's mode-`0600` private state so Resume and
autostart preserve the same value, and excludes them from the returned run
`env` and program registry. A safe mapping uses a non-secret placeholder:

```json
{
  "section": "LICENSE",
  "key": "LICENSE_KEY",
  "value": "{{program.secret.LICENSE_KEY}}"
}
```

The start request supplies the value separately:

```json
{
  "secret_variables": {
    "LICENSE_KEY": "write-only-value"
  }
}
```

For config-backed fields, the mapping value is the default value used for runs
unless the run provides an override. Config mappings may include an optional
`comment` for help text.

Mast also exposes mapped values as process environment variables. A mapping with
`"key": "DEVICE_ID"` becomes `DEVICE_ID=<resolved value>` in the program
environment.

For `.ini` config files, Mast also supports structured replacement by
`section` and `key`; for other config files, Mast performs placeholder
replacement on matching `{{key}}` tokens.
