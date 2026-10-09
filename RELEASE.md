# Releasing lazyrun

## How it works

Pushing a version tag starts `.github/workflows/release.yml` on that exact
revision. It reuses the normal CI workflow to run offline installer tests,
formatting, vet, race tests, fuzzing, Flask/Celery smoke tests, and the vulnerability audit. Only after every
gate passes does it build Linux amd64/arm64 packages, verify them, and upload
archives and SHA-256 checksums to a **draft GitHub Release**.

Publishing is a separate, manual action. A failed gate leaves the Git tag in
place but does not create a release. An upload failure can leave a partial draft;
do not publish it until the workflow succeeds.

The workflow uses GitHub's automatic `GITHUB_TOKEN`; no personal access token or
additional repository secret is required. GitHub Actions must be enabled, and
repository/organization policy must allow the draft job's `contents: write`
permission. Contributors who can push release tags can trigger this workflow;
restrict `v*` tag creation/update/deletion with a repository ruleset if needed.

## 1. Prepare

- Merge the intended changes, including the release workflow, into `main`.
- Check that normal CI is green and review changes and known limitations.
- Choose a new version: `vMAJOR.MINOR.PATCH`. For prereleases use
  `vMAJOR.MINOR.PATCH-alpha.N`, `-beta.N`, or `-rc.N` (for example,
  `v0.1.0-rc.1`). Numeric components must not have leading zeroes.
- Use a release candidate when you want feedback before a stable release.

## 2. Tag the release

From a clean checkout:

```sh
git switch main
git pull --ff-only
git status --short  # Must be empty; commit or stash local changes first.
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

Replace `v0.1.0` with your chosen version. Creating the tag is local; pushing it
starts the workflow. No workflow creates or pushes tags on your behalf.

## 3. Review and publish

1. In GitHub's **Actions** tab, wait for **Draft release** to succeed.
2. Open the draft under **Releases**. Review the automatically generated notes;
   add breaking changes, upgrade instructions, and known limitations as needed.
3. Confirm that these three assets exist, with the correct version:
   - `lazyrun_v0.1.0_linux_amd64.tar.gz`
   - `lazyrun_v0.1.0_linux_arm64.tar.gz`
   - `lazyrun_v0.1.0_SHA256SUMS`
4. Download all three assets into an empty directory and run
   `sha256sum -c lazyrun_v0.1.0_SHA256SUMS`. Extract the archive matching your
   machine and check `./lazyrun --version` and `./lazyrun --help`.
5. Review the prerelease checkbox (automatically set for alpha/beta/rc tags),
   then click **Publish release**. Check the public release page and downloads.
6. Test the [README installer](README.md) against the published release using
   `LAZYRUN_VERSION` and a disposable `LAZYRUN_INSTALL_DIR`, then verify the
   installed binary's `--version`. The installer URL works once `scripts/install.sh`
   is on GitHub's `main` branch. Default installation selects the latest stable
   release; until the first stable release is published, users must pin a
   published prerelease explicitly. Draft assets are not publicly installable.

CI inspects both binaries' target metadata and runs the packaged native binary's
version/configuration checks. On the amd64 CI runner, arm64 is cross-built and
inspected, not executed; test on an arm64 machine before publishing if available.
Checksums detect corruption; they are not signatures or build attestations.

## Failures and retries

- **Transient CI/network/upload failure:** use **Re-run failed jobs** in Actions.
  If the packaging artifact has expired, use **Re-run all jobs**. Retries replace
  assets in an existing draft; they never modify an already published release.
- **Code needs fixing:** merge the fix and tag a new version. Tags name immutable
  release revisions; do not move or reuse them. Remove an obsolete draft if one
  was created, and explain the skipped version in the next release's notes.
- **Problem found after publication:** publish a new patch release and document
  the issue. Do not overwrite released binaries or retag an existing version.

## Local packaging (does not publish)

For local preflight, provision the smoke venv as described in
[`examples/smoke/README.md`](examples/smoke/README.md), then run:

```sh
LAZYRUN_SMOKE_PYTHON=/absolute/path/to/venv/bin/python make release-check
make release VERSION=v0.1.0-rc.1
VERSION=v0.1.0-rc.1 bash scripts/check-release.sh
```

`make release` alone does not run the gates and can package a dirty working tree.
It never tags, pushes, signs, or publishes. `dist/` may contain previous builds;
only use the three files for your selected version, not `dist/*`.
