# Initial implementation decisions

## Scope

This pass implements M1 and an M2 headless runtime spike. It does not implement
supervisor detachment/reconnect, socket security, config synchronization, atomic
metadata persistence, disk logs, or the dashboard. Those remain M3–M5 work.
There is intentionally no CLI shortcut to run the in-process engine: that would
suggest persistence guarantees it cannot provide.

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
and cannot distinguish a executing descendant from our unreaped leader.

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

## Temporary logs and transport boundary

M2 retains a bounded raw-byte memory tail per latest run. It cannot stall on a
slow socket client because no socket client exists yet; capture only writes to
this bounded buffer. M4 replaces this with bounded disk capture, record cursors,
rotation gaps, disk-error reporting, and bounded transport/UI reads. Output is
not terminal-rendered in this milestone; ANSI/control sanitization remains a
release gate.

`internal/transport` contains vocabulary/envelopes only, not an IPC server.
Environment-bearing requests must never be diagnostic-logged. The run model
redacts environment fields in JSON; M3 config synchronization needs an explicit
control-message encoding for configured overrides rather than blindly marshaling
the redacted state model. M3 must also move definition synchronization and all
cross-client authority into the supervisor without tying runtime goroutines to
dashboard request contexts.
