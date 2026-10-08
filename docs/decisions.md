# Implementation decisions

## Scope

M1 provides the config/domain foundation; M2 proves the headless process-group
engine; M3 adds detached supervision, reconnect, config synchronization, private
IPC, and atomic metadata. Disk capture/rotation remains M4, the dashboard M5.
The headless CLI always goes through the same supervisor path the TUI will use;
there is no in-process execution shortcut with weaker lifetime guarantees.

Go 1.25 is the initial supported toolchain; the development environment has
1.25.3. Dependencies are pinned in `go.mod`/`go.sum`. The local module path is
provisional until the repository's public identity is decided.

## YAML: narrow substitution

Evaluated `github.com/jesseduffield/yaml` at `v2.1.0+incompatible`, the parser
family used by lazydocker. Its `UnmarshalStrict`/`Decoder.SetStrict` reject
unknown struct fields and duplicate keys in ordinary Go maps. `MapSlice` retains
mapping order, but `decode.go:mappingSlice` simply appends entries and does not
apply strict duplicate checks. In particular, the ordered representation and
strict representation do not provide the requirements together in one decode.
A second strict decode could be added, but that requires maintaining two matching
representations and still complicates explicit scalar validation and diagnostics.

Use `go.yaml.in/yaml/v3` nodes instead. Configuration explicitly checks mapping
shape, string keys, duplicates, allowed fields, and scalar types at each level,
while retaining source order and source-line errors. This is only a YAML parser
substitution; no lazydocker/Docker configuration layer was imported.

Anchors/aliases/merge keys are rejected deliberately rather than silently
expanding definitions or weakening duplicate-key validation. Regression tests
cover duplicates in both alias sections, across sections, in command/env/log
fields, and at the root. Configuration discovery/validation executes nothing.

## Linux process-group completion spike

`exec.Cmd.Wait` must not define complete group shutdown: a shell can exit while
its children remain alive. Conversely, `kill(-pgid, 0)` alone includes zombies
and cannot distinguish an executing descendant from our unreaped leader.

The prototype:

1. Enables `PR_SET_CHILD_SUBREAPER` in the owning runtime process before launch.
2. Starts the shell with `Setpgid`, disconnected stdin, and explicit output FDs.
3. Records boot identity plus PID/PGID and `/proc/PID/stat` start ticks.
4. Delays reaping the group leader, retaining its kernel PID/PGID reservation.
5. Scans group members, distinguishing executing processes from zombies. While
   live, it reaps identity-verified direct/adopted descendant zombies to avoid
   accumulating zombies during a long-running service.
6. Requires two identical zombie-only membership scans before completion. A
   single directory snapshot can miss a child created just before a parent
   exits. During these completion scans no owned group zombies are reaped;
   subreaping keeps newly orphaned descendants observable.
7. Reaps adopted descendants with explicit PIDs, then calls `exec.Cmd.Wait` for
   the leader, finishing bounded output collection before admitting replacement.

Stop verifies the still-owned leader before signaling the negative PGID. A
persisted PID or an arbitrary group found in `/proc` is never accepted as owned.
Only the runtime's own unreaped children qualify. There is no PID-file recovery
path in this spike. The model's persisted identity fields are diagnostic only.

The subreaper setting is process-wide. The dedicated supervisor must own all
managed waits; do not install a wildcard child reaper or share this engine with
code that changes child ownership/reaping. Proc scanning is intentionally simple
for the spike and needs performance evaluation with many simultaneous aliases.

Tests cover ordinary parent/child shutdown, an exited shell with a SIGTERM-
ignoring child, a rapid 26-generation orphan chain, concurrent duplicate starts,
independent aliases, and blocked replacement cancellation. These are evidence for
the Linux baseline, not a replacement for testing real reloaders or daemon
security. Escaped groups/daemonized descendants remain explicitly unsupported.

## M3 ownership, startup, and private storage

- One per-project runtime ownership `flock` is acquired before launching. A
  separate launcher lock serializes startup attempts. The ownership open-file
  description is transferred to the same executable via `ExtraFiles`, then the
  parent closes its copy. A state-directory lock also prevents dual engines or
  writers when clients choose different runtime locations for the same state.
- A bounded pipe handshake reports readiness/startup errors, rather than sleeping
  and hoping the daemon is ready. Concurrent clients wait for ownership/readiness;
  they never unlink a socket whose owner still holds the lock.
- The daemon uses `setsid`, disconnected stdin/stdout/stderr at bootstrap, and
  supervisor-owned pipe draining into a bounded diagnostic log thereafter.
  Restore `CLOEXEC` on inherited bootstrap descriptors immediately. A crash test
  caught the ownership-lock descriptor otherwise leaking into managed children.
- Runtime/state directories are capability-style dirfds. Absolute components are
  opened without following symlinks; ownership and writable ancestors are checked
  (root-owned sticky `/tmp` is permitted). App/project directories are private.
  File opens validate owner/type/0600/single-link before writing, and reject links.
- Socket bind/connect uses `/proc/self/fd/<dirfd>/control.sock`: Linux resolves it
  through the validated directory and avoids Unix socket pathname-length limits.
  The inode is also accessible at its normal runtime pathname. Removal happens
  only under ownership after refused/absent connectivity, or for the exact socket
  inode published by an exiting owner. Socket mode is private at bind time,
  not merely after chmod: repeated bootstrap tests caught initial probes racing
  the permission change. The temporary bind umask is restored before requests.
  Incompatible/malformed/live listeners are not stale sockets and must not prompt
  a second launch.
- XDG namespaces are expected to remain stable. The additional state lock catches
  a changed runtime namespace; deliberately changing both runtime/state locations
  while commands run is unsupported, not a project-registration feature.

## M3 protocol, configuration, and authority

The protocol is versioned JSON with a four-byte big-endian frame length (8 MiB
maximum), 32 simultaneous connections, and 10-second I/O deadlines. Unix peer
credentials must match the current user in both directions. Every connection
handshakes with the full project ID before operations; version mismatch is an
explicit compatibility error. The reported binary version is diagnostic; the
protocol version defines compatibility and must change for breaking wire changes.
Clients use fresh connections, never automatically retry lifecycle mutations.

Sync messages carry the exact validated YAML as bytes, retaining configured env
values/order without misusing the redacted state model. The supervisor reparses
against its canonical root. Synchronization updates all definitions atomically
with respect to lifecycle requests, but does not rewrite active snapshots or
restart commands. Removed active runs remain reachable; moved active runs retain
their original kind. The next accepted run uses current definition/shell/settings.

Start/restart messages carry the requesting client's environment as base64 byte
entries, preserving arbitrary Unix environment bytes through JSON. Configured
values override afterward. No environment-bearing requests are diagnostic-logged;
state/metadata omit env maps, and inherited snapshots are dropped after exec.
Accepted lifecycle work is not tied to a socket/client cancellation context.

## M3 durable intent and supervisor loss

Latest metadata uses an alias hash filename and a schema version. Writes use an
exclusive private temporary file, file fsync, atomic rename, and directory fsync.
Durable `starting` intent (including boot ID) is written **before exec**. If that
fails, execute nothing. Then persist PID/PGID/start ticks, stop intent, and actual
outcome. A failure after launch keeps capture/ownership active and exposes the
accepted run plus `metadataError`; a response error is not proof nothing started.

A replacement supervisor never turns a recorded identity into an owned command.
Known finished outcomes remain intact. Previous-boot or atomically absent groups
become historical exited runs with unavailable outcome where none was collected;
no exit code, signal, or end time is invented. For same-boot active records, use
`kill(-pgid, 0)` only as a nonmutating kernel existence probe: unlike `/proc`
enumeration, it cannot miss a child born during inspection. Occupied/reused groups,
permission failures, and incomplete launch identity remain `unknown` and block
start/stop/restart with manual-cleanup guidance. They are never signaled or reaped.
Corrupt/incompatible metadata prevents unsafe startup rather than being discarded.

## Temporary log adapter

M3 retains a bounded raw-byte memory tail per latest run and exposes byte cursors
with run IDs, truncation flags, and at most 64 KiB per read. Reads/slow sockets do
not own capture or command lifetimes. Historical memory output after supervisor
replacement is explicitly marked unavailable. Responses encode raw output bytes
as base64, preserving invalid UTF-8 without rendering terminal control sequences.
M4 replaces this adapter with bounded disk capture, timestamp records, rotation,
and disk-write stress/error tests. Full terminal sanitization remains an M5 gate.
