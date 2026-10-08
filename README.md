# lazyrun

A Linux-first, keyboard-driven Go TUI for a project's named services and tasks.
**Implementation in progress: there is no dashboard or detached supervisor yet.**

## Current status

- **M1:** nearest-project discovery, canonical identity, ordered and strict YAML
  configuration, domain/run models, initial protocol vocabulary, CLI validation.
- **M2 spike:** a headless Linux runtime with per-alias serialization, combined
  output capture, real shell outcomes, and graceful process-group stop/restart.
  Deterministic subprocess tests cover descendants and blocked replacements.
- **Next: M3:** private sockets/locking, detached supervisor, reconnect, config
  synchronization, and conservative reconciliation. Persistence and disk logs
  are not implemented. M4 adds disk rotation/cursors; M5 adds the dashboard.

The runtime currently runs **only in tests**, not through the CLI. Do not assume
commands survive terminal closure until the M3 acceptance gate is proven.

## Build and validate configuration

Requires Linux and Go 1.25 or later. No root privileges are needed.

```sh
make build
cd /path/to/project
/path/to/lazyrun/bin/lazyrun --check
```

`--check` walks upward to the nearest `lazyrun.yml`, validates it, and lists aliases
in configuration order. It executes nothing. Invoking without `--check` currently
reports that the dashboard is not implemented and exits unsuccessfully.

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

**Treat configuration as executable code.** Future starts run only on explicit
request, but checked-in commands must still be trusted. No `.env` loading or
interactive shell startup files are planned. Commands receive the requesting
client's environment followed by configured overrides, never a stale daemon
snapshot. Inherited environment values are not serialized into run metadata.

## Runtime spike boundaries

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

Output retention is a temporary in-memory latest-run tail, capped by the smaller
of `logs.maxBytes` and 2 MiB. It preserves partial lines and indicates truncation;
it has no disk persistence, timestamp chunks, or incremental cursor reads yet.
Raw output is not safe to render directly in a terminal; sanitization is an M5
requirement. Noninteractive programs may buffer output; Python can use `-u` or
`PYTHONUNBUFFERED=1`.

Commands that deliberately daemonize or escape their group are outside v1's
lifecycle guarantees. There are no supervisor-crash, logout, or reboot recovery
promises. Real Flask reloader/Celery and terminal-closure checks remain future
release gates, not guarantees established by the synthetic fixtures.

## Development

```sh
make test
make race
make vet
make check              # formatting + vet + race tests
```

Linux lifecycle fixtures run the test binary as a subprocess and do not require
Flask/Celery. Force-kill is used only by failure cleanup in the tests, never by
product Stop/Restart. No UI, Docker, or logging-framework dependencies are added
until their owning milestone needs them.
