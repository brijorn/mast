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
| `platform` | string | `windows`, `linux`, `darwin`, or `any` (optional; inferred from command) |
| `entry` | JSON string | `{"command":"run.sh","args":[]}` |
| `ini_values` | JSON string | `[{"section":"Settings","key":"DEVICE_ID","value":"{{phone.serial}}"}]` |
| `files` | file (multiple) | Each file part's filename is its relative path in the bundle |

Response: `201 Created` with the `Program` JSON object.

## Versioning

Mast uses a **latest-only** versioning model:

- Each program has a **slug** derived from its name.
- `registry.json` stores one current program entry per slug.
- Re-uploading the same program name replaces the current bundle entry and
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

---

## Instance cleanup

Instance workspaces can grow large for long-running programs. Mast manages
cleanup at two levels.

### Automatic cleanup on new start

When a new run is started for a device serial, Mast automatically removes the
workspace directories of previous `exited`, `failed`, or `stopped` runs for
that serial before creating the new instance. Runs that are still `running`,
`starting`, or `lost` are never touched.

This policy reclaims disk naturally when a phone switches programs or re-runs
the same program, without interfering with active 20–30 day sessions.

### Manual cleanup via API

To free the workspace of a specific completed run immediately:

```http
POST /api/runs/{id}/cleanup
```

Returns `400 Bad Request` if the run is still active. Lost runs can be cleaned
up only after Mast confirms the saved process is no longer alive. Returns the
updated `Run` object with `"workspace_cleaned": true` on success.

### Resume

`POST /api/runs/{id}/resume` re-runs the saved command in the same instance
workspace, preserving the same run ID and replacing the previous logs. Mast
uses this for `exited`, `failed`, `stopped`, or `lost` runs. By default, resume
uses the run's original starting config values. To change values for the resumed
attempt, send a JSON body with `variables`; those values are applied to the
process environment and rendered config file without changing the run's saved
starting defaults.

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

Mast caps each stdout/stderr stream to one retained file of up to 10 MiB. When
the file exceeds the cap, Mast keeps the newest bytes and records the logical
start offset in `run.json` so offset polling can continue. If a client asks for
an offset older than the retained window, Mast returns the retained window and
sets the corresponding reset flag.

### Autostart

`PUT /api/runs/{id}/autostart` stores a run-owned autostart flag:

```json
{"enabled": true}
```

When Mast starts, it automatically resumes autostart-enabled runs that are
`stopped` or `lost`, using the same run ID and instance workspace. Normal
`exited` and `failed` runs are not restarted automatically.

Manual `POST /api/runs/{id}/stop` disables autostart for that run. Mast's own
shutdown path stops active programs without clearing autostart, so configured
runs come back when Mast is launched again.

When Mast restarts while a run is active, it restores that run as `lost` rather
than `failed`, because Mast no longer knows whether the program itself failed.

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

When Mast starts a program, it normally runs the program command directly. However, if a program uses standard file formats (such as a `.py` script or `.jar` binary), the host machine can configure a wrapper/runner command to execute them.

This configuration is stored in the local host's `~/.mast/config.json` configuration file, which means it is specific to the machine running the program and does not need to be committed to the public repository.

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

Programs can contain dynamic placeholders in their configuration files (like `.ini`, `.toml`, `.cfg`, `.conf`) or program arguments.

### Template Placeholders
Placeholders are defined using `{{placeholder}}` token notation.

#### 1. Built-in Tokens
Built-in tokens are automatically populated by Mast depending on the executing phone. There are exactly two supported built-in tokens:
- `{{phone.serial}}` - Replaced with the target phone's serial number.
- `{{phone.node_id}}` - Replaced with the node ID of the host.

#### 2. Custom Tokens
Any other token (e.g. `{{license_key}}`, `{{resolution}}`) represents a custom variable.
- In the Runway UI, these are presented as editable input fields when starting a program run.
- For config-backed fields, the mapping value is the default value used for runs unless the run provides an override.
- Config mappings may include an optional comment for help text.
