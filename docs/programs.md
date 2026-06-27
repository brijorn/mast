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
  registry.json           – index of all registered programs (one entry per bundle)
  versions.json           – maps slug → current bundle ID
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

### Via API (path-based, legacy)

```http
POST /api/programs
Content-Type: application/json
{"path":"/absolute/server/path","entry":{"command":"run.sh"}}
```

This requires the program files to already exist on the server running Mast.

---

## Versioning

Mast uses a **latest-only** versioning model:

- Each program has a **slug** derived from its name.
- Only one bundle is kept as "current" per slug in `versions.json`.
- Re-uploading the same program name replaces the current bundle entry.
- Old bundle directories become orphans (they are not deleted automatically
  because a running instance may still be executing from them).

### Starting a program by slug

`POST /api/runs` accepts either a content-hash ID **or** a slug in the
`program_id` field. When a slug is given, Mast resolves it to the current
bundle automatically:

```json
{"program_id": "my-app", "serials": ["device-123"]}
```

This always starts the latest uploaded version of `my-app`.

### Detecting an update

`GET /api/runs` includes `"update_available": true` on any run whose bundle
differs from the current bundle for that program's slug. This means a newer
version of the program has been uploaded since this run started.

Running instances are never interrupted by an upload — they hold their own
copy of the bundle files in their instance workspace.

---

## Instance cleanup

Instance workspaces can grow large for long-running programs. Mast manages
cleanup at two levels.

### Automatic cleanup on new start

When a new run is started for a device serial, Mast automatically removes the
workspace directories of **all completed or failed** previous runs for that
serial before creating the new instance. Runs that are still `running` or
`starting` are never touched.

This policy reclaims disk naturally when a phone switches programs or re-runs
the same program, without interfering with active 20–30 day sessions.

### Manual cleanup via API

To free the workspace of a specific completed run immediately:

```http
POST /api/runs/{id}/cleanup
```

Returns `400 Bad Request` if the run is still active. Returns the updated `Run`
object with `"workspace_cleaned": true` on success.

---

## Orphaned bundles

When a program is re-uploaded, the old bundle directory (`bundles/<old-hash>/`)
is no longer referenced by `versions.json` but is not automatically deleted
because a running instance may still be using it. Once all runs that reference
the old bundle have completed and had their instances cleaned up, the old bundle
directory can be safely removed manually.

A future `DELETE /api/programs/{id}` endpoint may automate this.

---

## Custom Program Runners

When Mast starts a program, it normally runs the program command directly. However, if a program specifies a custom `platform` (such as `windows`) or uses standard file formats (such as a `.py` script or `.jar` binary), the host machine can configure a wrapper/runner command to execute them.

This configuration is stored in the local host's `~/.mast/config.json` configuration file, which means it is specific to the machine running the program and does not need to be committed to the public repository.

### Matching Order
When looking up a runner for a program, Mast evaluates the following in order:
1. **Platform name**: Looks up the program's registered platform in the `runners` map (e.g., `windows`).
2. **File extension**: Looks up the entrypoint command's file extension in the `runners` map (e.g., `.py` or `.exe`).
3. **Fallback default**: If no runner matches and the platform is `windows` on a `linux` host, Mast defaults to using `winerun`.

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
