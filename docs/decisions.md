# Implementation decisions

## Scope

M1 provides the config/domain foundation; M2 proves the headless process-group
engine; M3 adds detached supervision, reconnect, config synchronization, private
IPC, and atomic metadata. M4 adds bounded disk capture/rotation; M5 adds the
client-only dashboard.
The headless CLI and TUI always go through the same supervisor path;
there is no in-process execution shortcut with weaker lifetime guarantees.

Go 1.25 was the initial toolchain; M6 raises the minimum to patched Go 1.25.10.
Dependencies are pinned in `go.mod`/`go.sum`. The configured GitHub origin settled
the module/install identity as `github.com/nullco/lazyrun`.

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

## M4 bounded disk logs

The CLI/supervisor now use `internal/logstore`; the M2 memory tail remains only
for standalone runtime fixtures. Protocol version 2 adds timestamped records and
explicit initial-tail requests. It deliberately rejects older supervisors instead
of silently serving their memory-only retention as disk-backed logs. Byte cursors
and mandatory run IDs remain stable concepts; lifecycle mutations are unchanged.

Each alias has a private dirfd-backed `logs/<sha256(alias)>/` directory and four
fixed files. Small budgets use one to three active slots. Rotation truncates the
oldest slot before reuse, under a disk/index lock; no unbounded backup files or
unlinked files held by slow readers accumulate. Payload never exceeds the run's
`maxBytes`; physical file lengths exceed it by at most 336 bytes. Allocation-unit
filesystem overhead is separate. Record-index limits may rotate early on tiny
writes (at most 1,024 entries per slot), keeping memory bounded independently of
arbitrary configured disk budgets. Removed aliases' latest logs/metadata remain
on disk; there is no project-wide historical alias garbage collector in v1.

Headers carry format version, run-ID hash, immutable retention budget, and first
cursor. Records carry byte cursor, capture time, length, payload, and CRC32 over
both record header and payload. Chunks are at most 32 KiB; timestamps describe
capture chunks, not application emission times or exact per-line times. Both
streams share a pipe, so no stronger cross-stream ordering is promised.

Capture and disk IO have separate locks. An eight-chunk (256 KiB) queue evicts the
oldest pending chunk on overflow, keeping recent/final bytes without waiting for
disks or clients. Cursors still count discarded bytes, so subsequent retained
records reveal gaps. Setup/write failures disable further writes for that run;
pipe draining continues. Sticky `logError`/read `error` are visible even during an
active run and persisted on state transitions. Client reads never retry writes or
repair files. Final capture closes flush the queue, fsync files and directory,
then runtime persists final outcome and captured byte count. Local filesystem IO
can itself block; the queue isolates draining, not arbitrary filesystem hangs
from finalization/startup. Normal capture is not timed out as an escaped pipe just
because final disk sync takes longer than the pipe-drain timeout.

Restore checks format, run identity, size/record/index bounds, cursor ordering,
and checksums once to rebuild the bounded index. Torn/corrupt suffixes are not
served; verified prefixes remain available with errors. Missing or memory-only
logs are explicitly unavailable. A supervisor-loss run warns its final tail may
be incomplete, without inventing an outcome or granting process ownership.

Reads fetch at most 64 KiB of payload through the index; reconnect does not load
whole files. Each response includes concatenated raw data plus timestamped slices
with their original byte cursors, so mid-response gaps are unambiguous. Everything
is base64 in JSON. Initial tail reads are byte-bounded before counting lines
(default 1,000; zero means byte limit only). Capture times are always retained;
`logs.timestamps` only controls dashboard display.

## Retained-log navigation and search (protocol 3)

Protocol 3 adds indexed window reads and resumable literal searches; ordinary
raw cursor/tail reads and lifecycle authority remain unchanged. It deliberately
rejects live protocol-2 supervisors. Never remove locks/socket or auto-replace a
live supervisor during this upgrade. Old disk format/metadata remains readable.

Window reads count retained bytes backwards through the existing record index,
not byte-cursor distance across losses. Responses expose the oldest retained byte
and published end. The GUI maps emitted newlines back to raw cursors, ignoring
newlines inside discarded terminal strings, to anchor backward/forward paging.
Historical windows are 8 KiB: even a newline-only page cannot exceed the 10,000
line UI cap and evict the beginning that Home just requested. Huge lines use
bounded fragments; Home and G explicitly seek earliest retained/live tail.

Search is case-sensitive literal terminal text, limited to 256 query bytes.
Streaming KMP is linear, carries UTF-8/escape-parser state and raw positions
across capture records/requests, and resets at explicit gaps. It excludes
ANSI/control strings and display timestamps, with the same CR/tab/UTF-8/control
semantics as the GUI. A sanitizer-equivalence fuzz test guards this boundary.
Match positions include raw line starts and display columns, so horizontal
scrolling reveals matches in long lines; very large lines seek a match fragment.

Each request scans at most 256 KiB (disk lock only per 64 KiB read), returns at
most 64 matches, and carries a validated bounded continuation with the client.
No server-side search sessions, full-file buffers, persistent indexes, regex
backtracking or logged query terms. The GUI retains only the current match batch;
next pages resume scanning, while uncached previous navigation scans the prefix
for its last eligible match (including overlapping matches). That reverse case
is linear, not an instant indexed lookup; ordinary cached navigation is direct.
The published end is fixed for the search, while rotation/gaps can still overtake
it and are reported. Search cancellation prevents subsequent requests; an accepted
bounded disk read may finish. Filesystem stalls retain the existing IO caveat.

One coalesced search worker and one coalesced log/window worker use request
contexts, generations, run IDs and the bounded UI mailbox. Alias/run changes,
Details/minimum-size modes and quit cancel local requests; never capture or
supervision. An editable, byte-bounded prompt consumes lifecycle/quit letters as
text. Highlighting runs only on cropped viewport text, restores application
styles afterwards, and never expands an entire long line into styled cells.

## M5 dashboard and terminal boundary

Use `github.com/jesseduffield/gocui` at the exact pseudo-version used by the
inspected lazydocker snapshot (`8cd33929c513`), not the ancient tagged v0.3.0.
Its tcell backend supports simulation tests and true-color cells. The layout,
controller and filter are original code; no lazydocker implementation was copied.
No Docker/lazycore dependencies were added.

Visual feedback adds one empty row between the stacked panes and one empty column
before Logs/Details, without raising the 70×18 minimum. Enable gocui's GUI-level
`Highlight` (required for `SelFrameColor`) for a green focused frame/title. The
focused command list uses a cursor-aligned, full-width white-on-blue selection;
unfocused lists keep the `>` marker and original status colors. Lifecycle/outcome
colors never replace their textual labels or change process state. Metadata is
sanitized before adding fixed SGR styles; application log colors stay independent.
Rendered tcell simulation tests cover focus transfer, selection, help, and log
color preservation, in addition to geometry/scrolling and real-terminal tests.

Mouse reporting is enabled through the same gocui/tcell backend. Left click only
focuses a pane/selects a displayed command; lifecycle operations stay on explicit
keyboard actions. A bounded last-rendered alias map resolves scrolled row hits
without using a potentially reordered state snapshot. Borders/blank rows focus
without selecting, and gaps/footer do nothing. Help/minimum-size guards block
underlying navigation. The wheel targets the hovered pane and shares keyboard
scroll/follow behavior. Unit tests and actual SGR reports through a real PTY
verify selection, focus, wheel, modal blocking and keyboard action targeting.

The GUI depends on a narrow client interface, never runtime/exec/signal APIs.
The default CLI requires terminal stdin/stdout before connecting; explicit flags
remain headless. Poll state every 400 ms and selected logs every 200 ms, with
bounded request deadlines. A single log worker coalesces selection changes;
cancel its request context and tag results with a generation and run ID so stale
responses cannot contaminate another view. Details/minimum-size mode suspends
log reads, not capture. A normally paused log view keeps ingesting within its
bounds. Historical paging/search jumps instead use a bounded snapshot window;
G reloads the live tail.
One outstanding lifecycle request per dashboard prevents key-repeat queues;
server-side serialization still arbitrates other dashboards. Never retry a
mutation after response loss.

UI state belongs to the gocui event thread. Workers use a bounded 16-event
mailbox. One acknowledged UI wakeup is outstanding at a time: gocui's ordinary
`Update` spawns a goroutine per call and cannot cancel queued callbacks. Shutdown
cancels/waits for local workers before restoring the terminal. State-read failures
preserve a visibly disconnected last-known snapshot and disable actions. Read
polling may reconnect to the same endpoint, but does not launch a replacement
supervisor, resynchronize config, signal recorded PIDs, or retry mutations.
Reopen deliberately for configuration refresh or supervisor-loss reconciliation.

The selected view retains at most 10,000 logical lines AND 2 MiB of sanitized
text, including timestamp/style prefixes. A huge partial line is byte-bounded
independently of newline count. Eviction drops old strings/references and adjusts
paused line anchoring; a persistent UI-eviction banner distinguishes it from disk
loss. Long lines are horizontally scrollable rather than expanded into unbounded
wrapped rows. Only visible rows/columns are sent to gocui, not the entire retained
buffer; terminal cell arrays therefore remain screen-sized. Color state is
canonicalized in bounded prefixes across line eviction/horizontal clipping.

Audited gocui `escape.go`/`view.go`: it interprets SGR but also line erasure and
carriage-return overwrites; malformed escapes can become literal cells. Do not
rely on that parser as the security boundary. A separate streaming allowlist
admits only complete, validated SGR (including 256/true colors). CSI carry is at
most 128 bytes/20 parameters, UTF-8 carry at most three bytes. OSC/DCS/SOS/PM/APC
strings are dropped without buffering until BEL/ST. Strip all other C0/C1 and
Unicode format/bidi controls; normalize CR/CRLF, expand tabs and replace invalid
UTF-8. Final incomplete UTF-8 is visibly replaced; incomplete control strings
are discarded. Gaps reset parser carry so a lost terminator cannot suppress
later output. Apply the same filtering without ANSI to paths, config, names and
errors. Raw disk bytes and headless base64 responses are unchanged.

Unit/simulation tests and sanitizer fuzzing cover this boundary. Real PTY tests
exercise actual dashboard keys, task outcomes, environment snapshots, pause/follow,
resize/minimum mode, restart/stop, reconnect, quit/Ctrl-C, SIGTERM, crash and hangup.
These supplement, rather than replace, the M3 ownership/lifetime gates.

Full-suite stress also exposed a pre-existing bootstrap race: Linux path lookup
can pin the old socket inode just before a competing launcher unlinks it, then
`fstatat` returns valid owner/type/mode with zero links instead of ENOENT. Treat
that already-unlinked path as absent and re-probe under startup/ownership locks.
Wrong owners/types/modes and multiple links remain errors; opened file capabilities
still require exactly one link. Deterministic validation tests and repeated
concurrent-bootstrap integration tests cover this distinction.

## M6 release gates and real application findings

The symbol-level `govulncheck` scan of the original Go 1.25.3 reported reachable
standard-library findings (including conservative cross-platform reports). Raise
the module minimum to Go 1.25.10 rather than suppressing findings; the patched
symbol scan is clean. Keep the audit tool pinned and re-run it against the live
vulnerability database before releasing. Uncalled package/module findings are
reported separately and are not represented as reachable exploits.

Changing the provisional module path to the configured `github.com/nullco/lazyrun`
origin changes import/install identity, not project IDs, state namespaces or wire
compatibility. Display version comes from a release linker override, the module
version for `go install @tag`, or source VCS revision/dirty metadata. Protocol 3
remains the compatibility authority. Static CGO-disabled Linux amd64/arm64
packages use trimmed paths, disabled build VCS data, normalized archives and
checksums, retaining Go/dependency license and NOTICE texts. An explicit source
allowlist excludes local venvs/brokers/artifacts.
The repository owner chose MIT for lazyrun; release archives include `LICENSE`
alongside dependency notices. Publication/tagging and native arm64 validation
remain deliberate owner steps, not side effects of packaging or CI.

Opt-in smoke tests use pinned Python 3.12 packages, the real Werkzeug stat
reloader, and real Celery prefork/threads workers with Kombu filesystem transport.
No Redis, root privileges, mock reloader, monkey-patched pool signal handler or
production broker is required. Verify HTTP across reconnect/reload/restart, all
ordinary group descendants after stop, task publication/execution, final logs,
and canceled replacements after the bounded restart wait. The idle-stop gate
uses real Celery `inspect active` acknowledgement: a child's done file precedes
parent-side result collection and was insufficient under concurrent stress.

The smoke exposed an important limit: process-group SIGTERM also reaches default
Celery prefork children. With the tested versions they exit on SIGTERM and can
abort a task even while the parent announces `Warm shutdown`; parent handling of
child loss can exceed three seconds. Do not hide this by silently switching to
parent-only signaling. Tests cover idle prefork shutdown, busy prefork child loss,
and busy threads-pool warm completion with a canceled restart. Framework-internal
signals/shutdown behavior are not lazyrun force-kill, and the worker's shell
outcome is not a promise that every Celery job succeeded.

Final rendering stress found that zero-width combining marks could bypass a
column-only viewport limit (gocui stores cells per rune). Bound normal combining
clusters to four marks per visible base, and emit canonical styles only when a
visible rune is emitted; pure style floods cannot grow the cell array. Human
CLI diagnostics/`--check` escape control and bidi characters, while raw logs and
JSON retain their original bytes. Empty/repeated alias/run-ID flags now fail
before any supervisor connection rather than silently falling back to the
interactive default or choosing the last value.
