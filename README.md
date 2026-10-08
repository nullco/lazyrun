# lazyrun

A Linux-first, keyboard-driven Go TUI for a project's named services and tasks.
**Implementation in progress: detached supervision and a headless client work;
the dashboard and disk logstore are still to come.**

## Current status

- **M1:** nearest-project discovery, canonical identity, ordered and strict YAML
  configuration, domain/run models, initial protocol vocabulary, CLI validation.
- **M2 spike:** a headless Linux runtime with per-alias serialization, combined
  output capture, real shell outcomes, and graceful process-group stop/restart.
  Deterministic subprocess tests cover descendants and blocked replacements.
- **M3:** detached per-project supervisor, private versioned IPC, reconnect,
  configuration synchronization, atomic latest-run metadata, and conservative
  supervisor-loss handling. Real PTY tests cover terminal closure/client death.
- **Next: M4:** bounded disk capture, timestamp records, rotation/gaps, and log
  stress/error handling. **M5** adds the dashboard; **M6** hardens the release.

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
lazyrun --logs api              # bounded JSON response; data is base64
lazyrun --logs api --run-id RUN_ID --after CURSOR
```

Choose one action per invocation. All output is JSON except `--check`/help.
Log responses include the run ID, `next` byte cursor, and `truncated` flag; reads
are at most 64 KiB. Reuse `next` with the same run ID. Raw output is base64 rather
than terminal-rendered, so control sequences cannot execute on display.

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
- `tail` must be nonnegative; `maxBytes` must be positive. Log settings describe
  the planned disk/UI behavior, not a disk logstore that exists today.
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

Output retention is still a temporary in-memory latest-run tail, capped by the
smaller of `logs.maxBytes` and 2 MiB. It survives client reconnects, supports byte
cursors/partial lines, and reports gaps, but has no disk persistence or timestamp
chunks yet. Supervisor replacement reports historical output as `unavailable`
instead of silently pretending an empty stream was retained. M4 adds disk logs;
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
snapshots, metadata failures, supervisor loss, and real controlling-terminal
hangup. No Flask/Celery installation is required. Test cleanup may force-kill its
verified fixtures; product Stop/Restart never escalates beyond SIGTERM.
The Make targets disable Go's result cache (`-count=1`): subprocess builds can
change even when the linked test runner itself has not changed.
