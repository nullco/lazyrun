# lazyrun

A Linux-first, keyboard-driven Go TUI for a project's named services and tasks.
**Implementation in progress: detached supervision, bounded disk logs, and a
headless client work; the dashboard is still to come.**

## Current status

- **M1:** nearest-project discovery, canonical identity, ordered and strict YAML
  configuration, domain/run models, initial protocol vocabulary, CLI validation.
- **M2 spike:** a headless Linux runtime with per-alias serialization, combined
  output capture, real shell outcomes, and graceful process-group stop/restart.
  Deterministic subprocess tests cover descendants and blocked replacements.
- **M3:** detached per-project supervisor, private versioned IPC, reconnect,
  configuration synchronization, atomic latest-run metadata, and conservative
  supervisor-loss handling. Real PTY tests cover terminal closure/client death.
- **M4:** bounded disk capture, timestamp records, incremental/tail reads,
  rotation gaps, and visible disk/queue failures without blocking pipe draining.
- **Next: M5** adds the dashboard; **M6** hardens the release.

The headless CLI uses the detached supervisor, not an in-process execution path.
Opening it starts no configured command; explicit start/restart requests do.

## Build and validate configuration

Requires Linux and Go 1.25 or later. No root privileges are needed.

```sh
make build
cd /path/to/project
/path/to/lazyrun/bin/lazyrun --check
```

`--check` walks upward to the nearest `lazyrun.yml`, validates it, and lists aliases
in configuration order. It executes nothing and does not launch a supervisor.
Without `--check`, the client connects or auto-launches a supervisor, synchronizes
configuration, and prints state as JSON. No dashboard is drawn yet.

### Headless workflow

```sh
lazyrun --state                 # default; starts no commands
lazyrun --start api             # uses this shell's current environment
lazyrun --stop api              # SIGTERM only; query state for completion
lazyrun --restart api           # graceful stop, then current definition
lazyrun --logs api              # recent tail; bounded JSON, data is base64
lazyrun --logs api --tail 20     # recent lines, still bounded by bytes
lazyrun --logs api --after 0     # oldest retained bytes
lazyrun --logs api --run-id RUN_ID --after CURSOR
```

Choose one action per invocation. All output is JSON except `--check`/help.
Log responses include the run ID, `next` byte cursor, `truncated` flag, and
capture-time `records` (each with its own byte cursor and timestamp). Reads carry
at most 64 KiB of raw output. Initial reads use `logs.tail` (default 1,000 lines);
`--tail 0` disables the line limit, not the byte limit. Reuse `next` with the same
run ID and `--after`; `--tail` and `--after` are mutually exclusive. Raw output is
base64 rather than terminal-rendered, so control sequences cannot execute on
display. `error`/state `logError` report retention problems; `truncated` explicitly
marks rotation or dropped-output gaps.

Quitting, crashing, or closing the client's terminal leaves the supervisor and
commands running. There is no bulk action or supervisor shutdown command in v1.
A request can be accepted even if its response is lost: query `--state`, don't
blindly retry a mutation. Metadata failures after launch return an error **and**
the accepted run's identity. A blocked restart cancels its replacement, never
launching later unexpectedly.

The module name is provisionally `lazyrun`: no repository remote has been set.
A public `go install` path will be chosen before release.

## Configuration

```yaml
version: 1
name: my-project         # optional; defaults to the root directory name
shell: /bin/sh           # optional; absolute executable path, invoked with -c
services:
  api:
    command: .venv/bin/python -u -m flask run --debug
    env:
      FLASK_APP: app
      PYTHONUNBUFFERED: "1"
tasks:
  tests:
    command: .venv/bin/python -m pytest
    cwd: .               # relative to the configuration directory
logs:
  timestamps: false
  tail: 1000
  maxBytes: 10485760
```

See [`examples/flask/lazyrun.yml`](examples/flask/lazyrun.yml) for Flask/Celery.
The example is configuration only: it does not install or bundle a Flask app.

- Services/tasks may be omitted or written as `{}`; null/sequence sections fail.
- Aliases must be unique across both collections and nonempty printable strings.
- Unknown fields, duplicate keys at every level, unsupported versions, empty
  commands, invalid scalar types, and multiple YAML documents fail clearly.
- Configuration is limited to 1 MiB. Anchors, aliases, and merge keys are not
  supported: definitions must be explicit.
- Environment values are strings; quote numbers/booleans. Empty values are valid.
  Names cannot be empty or contain `=`/NUL; values cannot contain NUL.
- `cwd` defaults to the root; relative paths resolve against it. Absolute paths
  are allowed. Directory/executable existence is checked at launch, not parsing.
- `tail` must be nonnegative; `maxBytes` must be positive. Retention budgets are
  snapshotted per run; config changes apply to the next run. `timestamps` controls
  future dashboard display, not whether capture times are recorded.
- Symlinked invocation directories resolve to the physical project hierarchy.
  Distinct clones/worktrees have distinct identities.

**Treat configuration as executable code.** Starts run only on explicit
request, but checked-in commands must still be trusted. No `.env` loading or
interactive shell startup files are planned. Commands receive the requesting
client's environment followed by configured overrides, never a stale daemon
snapshot. Inherited environment values are not serialized into run metadata.

## Runtime lifecycle and storage

The headless engine uses `/bin/sh -c` by default, `/dev/null` stdin, a new process
group, and a continuously drained pipe shared by stdout/stderr. It records the
shell's actual exit code or signal; tasks and services differ in presentation,
not automatic restart behavior. There is no PTY or interactive input.

Stop sends only `SIGTERM`, with no automatic force-kill. Restart waits up to
three seconds by default and cancels its replacement if the old group is still
alive or cannot be verified. Repeated requests do not queue replacements.
Cancellation of a caller's wait does not cancel the command.

On Linux the engine enables **process-wide child-subreaping**. It pins the group
leader's identity by delaying its reap, tracks ordinary descendants through
`/proc`, and reaps adopted zombies without wildcard waits. It is intended to run
inside the dedicated supervisor, not an arbitrary embedding application. That
process must not independently reap its children or change subreaper behavior.
Restricted/unreadable `/proc` causes conservative `unknown` state rather than
unsafe signaling or overlap. See [implementation decisions](docs/decisions.md).

Runtime socket/locks are in `$XDG_RUNTIME_DIR/lazyrun/<project-id>/`; when unset,
the fallback is an owner-validated private `/tmp/lazyrun-<uid>/` directory.
Metadata and a bounded (1 MiB) diagnostic log live in
`$XDG_STATE_HOME/lazyrun/<project-id>/`, defaulting to
`~/.local/state/lazyrun/<project-id>/`. Nothing is stored in the checked-in project.
Directories/files are private (0700/0600); unsafe links/permissions fail closed.
Keep XDG locations stable while a supervisor runs. State ownership also prevents
a changed runtime directory from creating a second writer for the same state;
changing **both** namespaces while running is unsupported.

Config updates affect the next run only. Active removed aliases remain visible
and stoppable until completion; active moved aliases keep their original kind.
Every reconnect synchronizes configuration; there is no live watcher.

Latest-run logs live under the project's state directory in `logs/<alias-hash>/`.
A four-slot file ring caps retained payload at `logs.maxBytes` (default 10 MiB),
with at most 336 bytes of additional file-format overhead. Tiny budgets use fewer
slots. Rotation and a bounded record index may retain less than the byte ceiling,
particularly for tiny writes. A new run replaces the previous run's output.

Capture uses a separate bounded queue (eight 32 KiB chunks). Disk writes/reads and
slow clients never own pipe draining. If disks cannot keep up, older queued output
is dropped explicitly; write failures disable retention for that run but continue
draining. Inspect `logError`/log `error`, not just the command's exit status.
Finalized files are synced before final metadata; verified logs remain readable
across supervisor replacement. Abrupt loss may discard queued output or leave a
torn last record, which is reported rather than trusted. Missing/older memory-only
logs are `unavailable`, never silently substituted from another run.

M5 must sanitize terminal rendering. Noninteractive programs may buffer output;
Python can use `-u` or `PYTHONUNBUFFERED=1`.

Commands that deliberately daemonize or escape their group are outside v1's
lifecycle guarantees. There are no supervisor-crash, logout, or reboot recovery
promises. After supervisor loss, potentially surviving runs become `unknown` and
block start/stop/restart; recorded PIDs never grant signaling ownership. Available
metadata is retained without fabricated exit codes. See
[troubleshooting](docs/troubleshooting.md) before attempting manual cleanup.
Real Flask reloader/Celery smoke tests remain release gates.

## Development

```sh
make test
make race
make vet
make check              # formatting + vet + race tests
```

Runtime fixtures use subprocesses; supervisor integration tests build the real
binary (also race-instrumented under `go test -race`). They test concurrent launch,
reconnect, private paths, stale sockets, compatibility, config changes, environment
snapshots, metadata failures, supervisor loss, real controlling-terminal hangup,
and disk-log reconnect/rotation. Logstore tests cover tiny budgets, huge/partial
lines, invalid bytes, checksums/torn records, queue overload, ENOSPC/short writes,
private paths, and bounded indexes. No Flask/Celery installation is required. Test cleanup may force-kill its
verified fixtures; product Stop/Restart never escalates beyond SIGTERM.
The Make targets disable Go's result cache (`-count=1`): subprocess builds can
change even when the linked test runner itself has not changed.
