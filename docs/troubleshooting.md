# Troubleshooting supervision

## Install / version

Use Linux and Go 1.25.10 or a newer patched toolchain. For a local checkout use
`make build` or `go install ./cmd/lazyrun`; the public install path is
`github.com/nullco/lazyrun/cmd/lazyrun` after the reviewed source/tag is published.
`--version` identifies the client binary, not an upgrade of a live supervisor.
Use [the release checklist](release.md) for static packages and checksums.

## First checks

Run `lazyrun --check` from the project (or a subdirectory). This validates config
without launching a supervisor or command. `lazyrun --state` connects and shows
current definitions separately from latest-run snapshots, including errors,
PID/PGID/start ticks, and boot identity. It never starts a configured command.

If a request returns an error, check state before retrying: a disconnect or a
metadata failure can happen **after** a start/restart was accepted. The CLI prints
any returned run identity even when it exits unsuccessfully. There are no implicit
mutation retries.

## Dashboard / terminal

Run plain `lazyrun` in a terminal; use `--state` when piping output or scripting.
A useful dashboard requires at least 70x18 cells. Smaller sizes disable lifecycle
keys but still allow `q` / Ctrl-C; resizing back preserves commands and selection.
Press `?` for keys, `2`/`3` to select commands, and Enter to scroll their details.
Errors appear as nonblocking notifications; the latest full notification is also
in Details (scroll down). `!` next to a command means a run/metadata/log error.

Logs soft-wrap to the pane width, including long lines. Up/Down, `j/k`, the
mouse wheel and PgUp/PgDn scroll visual rows, not whole application lines.
Resizing reflows the text; application colors are retained across wraps.
Manual log scrolling pauses follow, not collection. `G` resumes following;
Left/Right scroll Details horizontally. `[output truncated or dropped]` marks
missing disk bytes. The dashboard-buffer eviction banner means the independent
10,000-line/2 MiB UI ceiling was reached; disk retention may still contain more.
Scroll past the loaded top to page in older retained output, or press `Home` to
jump to the earliest retained bytes; `G` reloads and follows the live tail. Paging
uses bounded windows rather than accumulating whole logs. Very long lines can
span page fragments. A "beginning not retained" title/notification means rotation
or loss removed the original beginning; there is nothing earlier to load.

Focus the selected command's **Logs pane** first (Enter or click inside Logs).
Only there does `/` open a literal, case-sensitive search across all retained
output of the selected latest run. The prompt stays inside Logs. Enter submits;
`n`/`N` navigate highlighted matches, Esc clears/cancels, and `G` returns to live
follow. Project, Services, Tasks and Details do not handle these log-search keys.
Leaving Logs cancels in-flight searches; completed results remain available when
returning to the same command's Logs. Search ignores ANSI/control strings
and UI timestamps, expands tabs, and replaces invalid UTF-8 like the dashboard.
Queries are at most 256 UTF-8 bytes; no regex or multiline matching. The prompt
accepts text and Backspace; Esc/Ctrl-C cancels without executing lifecycle keys.
Search progresses asynchronously in bounded 256 KiB requests. Cancellation stops
further requests (an already accepted bounded read can finish); disk stalls are
not given an instant-response guarantee. The search has a published-end snapshot;
submit a new search to include later output. Rotation during search may remove
matches or create gaps, which are reported rather than silently searched across.

Use `--logs --after` for bounded raw reads. ANSI colors are supported but terminal
control strings are stripped; progress carriage returns become separate lines.

A disconnected dashboard keeps last-known state, disables actions, and retries
only reads against the same endpoint. It does not launch a new supervisor or
retry a start/restart. Reopen deliberately for reconciliation/config refresh;
inspect unknown runs before cleanup. Quitting, Ctrl-C, or closing the terminal
never requests command stop.

## Private paths and compatibility

Runtime files live under `$XDG_RUNTIME_DIR/lazyrun/<project-id>/` or the private
`/tmp/lazyrun-<uid>/<project-id>/` fallback. Metadata/diagnostics use
`$XDG_STATE_HOME/lazyrun/<project-id>/` or `~/.local/state/lazyrun/<project-id>/`.
The project ID is the SHA-256 of its canonical absolute root path.

- Keep XDG runtime/state locations stable between clients. A changed runtime
  location cannot acquire state ownership while the original supervisor runs.
  Changing both namespaces while commands run is unsupported.
- Directory symlinks, unexpected ownership, writable unsafe ancestors, nonprivate
  file modes, or hardlinks are refused. Inspect the reported path instead of
  recursively changing permissions or deleting a shared temporary directory.
- An ownership lock with an unreachable socket is not permission to delete it or
  launch a replacement. It may be initializing or unhealthy; inspect the owner.
- An incompatible protocol is not a stale socket. Use a compatible binary or
  deliberately stop/manage the old instance after checking its commands.
- `diagnostic.log` is supervisor-only logging, bounded at approximately 1 MiB.
  It is separate from command output; requests/environments are never logged.

There is no multi-project registry or public supervisor-shutdown action yet.
An idle supervisor remains available for reconnect. For manual maintenance,
identify the exact process by executable, internal-mode root argument, user, and
Linux process start identity; don't act on a PID from an old note alone.

## A run is stuck stopping / restart was blocked

Stop only sends SIGTERM to the owned process group. A command ignoring SIGTERM
remains `stopping`; no automatic SIGKILL follows. Restart waits three seconds by
default, then cancels its replacement. It does not queue a start for later.
Ordinary descendants can keep a group alive after its shell exits.

Celery-specific caution: group-wide SIGTERM reaches default prefork children and
can interrupt jobs despite a parent `Warm shutdown` message. Handling child loss
may exceed the restart wait; a blocked replacement is canceled, not delayed.
The threads smoke demonstrates warm in-process task completion, not a universal
pool guarantee. Verify your exact worker/pool/tasks; see
[the real Flask/Celery smoke findings](../examples/smoke/README.md).

Inspect the command's shutdown behavior. Manual intervention outside lazyrun is
your choice and requires verifying the current process/group identity. An old
PID/PGID may have been reused; never blindly paste a stored ID into `kill`.

## Supervisor loss / unknown runs

Transparent crash recovery is not promised. Children may exit when their output
pipe closes, or remain alive and unmanaged. Opening a new client can launch a
replacement only after ownership is free, then conservatively inspects durable
metadata (an existing disconnected dashboard does not auto-launch one):

- Already collected outcomes are preserved.
- Previous-boot or provably absent groups are historical, not running. If the old
  supervisor never reaped the shell, its exit code/signal/end time is unavailable.
- Occupied groups (including potentially reused PIDs), permission failures, and
  incomplete pre-launch identity remain `unknown`. Start/stop/restart are blocked;
  a stored identity **never** authorizes the new supervisor to terminate it.

For cleanup:

1. Preserve/inspect metadata and diagnostics. Inspect live processes using their
   executable, command, working directory, boot/start identity, and group members.
   Do not rely on the stored PID alone.
2. Manually deal with only processes you have positively identified as yours.
   If identity is ambiguous, leave the blocker in place rather than killing an
   unrelated process or starting a possible duplicate.
3. Once no old execution can survive, deliberately stop the replacement supervisor
   (after identifying it) and reopen the project. Reconciliation can then recognize
   an absent group. A running replacement does not automatically clear unknown
   records based on later process disappearance.
4. An incomplete intent has no durable PID and cannot prove absence automatically.
   Only after verifying cleanup, back up and remove the **one alias record** while
   no supervisor owns the state. Records are `<sha256(alias)>.json` within that
   project's state directory. Corrupt/incompatible records similarly require
   inspection; do not erase all state just to suppress an error.

Never delete an ownership lock file while its owner is alive: a new inode can
create a second lock domain. Do not recursively remove arbitrary `/tmp` paths.

## Missing output or disk failures

Output is a bounded disk ring in the project's state directory under
`logs/<sha256(alias)>/`. Only the latest run is retained. Initial CLI reads use
`logs.tail` and a 64 KiB byte limit; try `--tail 0` for a byte-only tail, or
`--after 0` to read from the oldest retained bytes. For incremental reads resume
from the response's `next` with the same run ID. Use a new run ID after rerun.

`truncated` means rotation or dropped output overtook the requested cursor, or
there is a gap between returned records. Records retain their own byte cursors
and capture times. Smaller retention windows are normal with rotation or the
bounded record index; `maxBytes` is a ceiling, not a guaranteed history length.
Finalized disk logs survive supervisor replacement. Logs from older memory-only
versions or missing files are `unavailable`; never substitute another run's logs.
After abrupt supervisor loss, the final queued/torn suffix may be missing.

Noninteractive output can be buffered: try Python `-u`/`PYTHONUNBUFFERED=1`.
stdin is `/dev/null`; prompts, PTYs, and interactive commands are unsupported.
Raw output is base64 in the headless CLI so terminal control sequences aren't
rendered. Do not decode untrusted output straight into a terminal.

A log `error` or state `logError` means retention failed or the disk queue
could not keep up. Commands still drain output; a successful exit is not proof
all output was retained. Queue overflow evicts older pending output and continues
writing. Setup/write failures stop retention for that run; fix disk space or
unsafe permissions and explicitly rerun after inspecting state. Never delete or
replace live ring files to try to repair capture. Read errors/checksum failures
are visible, and unverified data is not returned during recovery. Logs themselves
may contain secrets; don't share raw log files casually.

Protocol version 3 is required for the current dashboard's paging/search APIs.
Protocol 2 introduced disk logs but cannot serve the new navigation requests. An old live supervisor is not a
stale socket: don't delete its locks/socket. After verifying/stopping its commands,
identify and stop the old supervisor deliberately before using the new binary.

A failed durable launch-intent write executes nothing. A metadata failure after
launch/stop is reported as `metadataError`, but capture continues and the process
remains owned. Fix disk space/permissions rather than repeatedly starting it.
Latest-run metadata is atomically replaced; interrupted private temporary files
are left for inspection, not blindly cleaned from shared locations.
