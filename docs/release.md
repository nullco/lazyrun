# Linux release checklist

Repository/module: `github.com/nullco/lazyrun`. Supported binary targets are Linux
amd64 and arm64. Native lifecycle/smoke gates run on Linux amd64; arm64 is
cross-built, not claimed to have passed native runtime tests here.

## Build / install

Use Go **1.25.10 or a newer patched toolchain**. The module minimum was raised
after the original Go 1.25.3 vulnerability scan; default Go toolchain selection
can download the required patch release. For a checked-out tree:

```sh
make build                         # bin/lazyrun, static CGO-disabled build
go install ./cmd/lazyrun            # $(go env GOPATH)/bin or your GOBIN
bin/lazyrun --version
```

After a reviewed commit/tag has actually been published to GitHub:

```sh
go install github.com/nullco/lazyrun/cmd/lazyrun@latest
# Prefer an actual published tag for repeatable installs, e.g. @v0.1.0.
```

This documentation does not imply a tag has already been published. Release
builds embed an explicit version; `go install @tag` uses Go's embedded module
version; source builds show the VCS revision/dirty state or `dev` when absent.
The wire protocol (currently 2), not the display version, defines compatibility.
Replacing a binary does not stop commands or upgrade an already-running supervisor.

## Required validation

```sh
make check                         # gofmt, vet, uncached full race suite
# Prepare the isolated venv from examples/smoke/README.md first:
LAZYRUN_SMOKE_PYTHON=/absolute/venv/bin/python make release-check
make stress                        # repeated lifecycle/IPC/security/log/PTY gates
make fuzz                          # bounded terminal parser fuzzing
```

`release-check` includes the opt-in real Flask/Celery smoke tests and pinned
`govulncheck`; it requires network access for the vulnerability database/tooling.
The ordinary Go tests do not install Python. Check warnings as well as command
exit codes: prefork task completion is **not** promised by group SIGTERM; see
[the observed real-worker behavior](../examples/smoke/README.md).

The CI workflow runs formatting/vet/race, fuzzing, real smoke and vulnerability
gates before uploading development packages. Actions/tooling are pinned and CI
has read-only repository permissions. It never creates a tag or GitHub release.

## Local packaging (no publication)

```sh
make release VERSION=v0.1.0-rc.1
(cd dist && sha256sum -c lazyrun_v0.1.0-rc.1_SHA256SUMS)
```

`dist/` contains static Linux amd64/arm64 tarballs and SHA-256 checksums. The
packager disables build VCS metadata, strips local source paths, normalizes
archive timestamps/owners/modes, and includes only an explicit documentation/
example allowlist (no local venvs, brokers, task artifacts or secrets). Bundled
Go runtime and linked module license/NOTICE texts are retained under `third-party/`.
Two builds
from the same inputs, toolchain and version should yield identical tarballs.
Checksums detect accidental changes; they are not signatures or proof of trust.

Before publishing:

- Review the working tree, choose the release version/tag and pass all gates.
- Choose a repository license; none is silently granted by this implementation.
  Review the bundled dependency notices and any additional distribution obligations.
- Test the native arm64 runtime before claiming native arm64 validation.
- Inspect/extract the archives, run the native binary's `--version` / `--check`,
  and verify checksums. Never distribute a dirty/unreviewed build as a final tag.
- Deliberately tag/push/publish using your normal review process. These scripts
  do **not** commit, tag, push, upload a public release, or sign artifacts.

## v1 limits to retain in release notes

Linux with readable `/proc` and Unix process groups; one discovered project per
client; explicit starts only; latest-run history only; stdin `/dev/null`; no PTY,
interactive input, dependencies, readiness ordering or auto-restarts. Stop is
process-group SIGTERM only; no automatic force-kill or queued delayed replacement.
Ordinary reloaders/prefork descendants are managed; daemonized/escaped groups are
not contained. Clients may exit/crash/lose their terminal without stopping runs.
Logout/reboot and transparent supervisor-crash recovery are not guaranteed.
Potential survivors after supervisor loss become unknown/unmanaged and require
identity-verified manual cleanup, never signaling from a stored PID. Logs may
contain secrets and retention can fail/drop bytes with explicit warnings. See
[troubleshooting](troubleshooting.md).
