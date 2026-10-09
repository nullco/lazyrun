# lazyrun

A terminal dashboard for your project's services and tasks. Linux only.
Aims to offer a familiar UX inspired by [lazydocker](https://github.com/jesseduffield/lazydocker)
and [lazygit](https://github.com/jesseduffield/lazygit).

## Features

- Start, stop, and restart named commands from one dashboard.
- Commands keep running when you close the dashboard; reconnect anytime.
- Wrapped, colored logs with live follow, history paging, and retained-log search.
- Keyboard and mouse navigation, adaptive pane layouts, and a headless CLI.
- Per-project configuration in `lazyrun.yml`. Nothing starts automatically.

## Quick start

Requires Linux. Building from source also requires Go 1.25.10 or newer.

### 1. Install

Install the latest stable release with one command:

```sh
curl -fsSL https://raw.githubusercontent.com/nullco/lazyrun/main/scripts/install.sh | sh
```

The installer detects Linux amd64/arm64, verifies the archive's SHA-256 checksum,
and installs to `~/.local/bin` without sudo or shell profile changes. If needed,
add that directory to your shell's `PATH`. Requires `curl` and standard Linux
packaging tools; Go is not required. Rerun the command to upgrade.

To choose a version (including a prerelease) or installation directory:

```sh
curl -fsSL https://raw.githubusercontent.com/nullco/lazyrun/main/scripts/install.sh \
  | LAZYRUN_VERSION=v0.1.0-rc.1 LAZYRUN_INSTALL_DIR="$HOME/bin" sh
```

Piping a URL into `sh` executes downloaded code. [Review the installer](https://github.com/nullco/lazyrun/blob/main/scripts/install.sh)
first, or download and inspect it before running `sh install.sh`. Checksums detect
corruption, not malicious releases. Installation requires a published release;
`latest` excludes prereleases and drafts.

Alternatively, download your architecture's archive and the matching `SHA256SUMS` file from
[GitHub Releases](https://github.com/nullco/lazyrun/releases). For example, for
`v0.1.0` on amd64 (`x86_64`), from the download directory:

```sh
# Verify the downloaded archive against its checksum entry.
grep '  lazyrun_v0.1.0_linux_amd64.tar.gz$' lazyrun_v0.1.0_SHA256SUMS | sha256sum -c -
tar -xzf lazyrun_v0.1.0_linux_amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 0755 lazyrun "$HOME/.local/bin/lazyrun"
export PATH="$HOME/.local/bin:$PATH"
lazyrun --version
```

Use your selected release version, and `arm64` for `aarch64` machines. Extract
into an empty directory. Release binaries do not require Go.

Alternatively, build from source:

```sh
git clone https://github.com/nullco/lazyrun.git
cd lazyrun
make build
export PATH="$PWD/bin:$PATH"
```

### 2. Configure your project

Create `lazyrun.yml` in your project, replacing these commands with your own:

```yaml
version: 1
services:
  api:
    command: python3 -u -m http.server 8000
tasks:
  tests:
    command: go test ./...
```

Commands run from the configuration directory and inherit your shell's environment.

### 3. Launch

```sh
cd /path/to/project
lazyrun --check
lazyrun
```

Select a command, then press `S` to start/run, `s` to stop, or `r` to restart.
Press `R` in any pane to reload `lazyrun.yml` without restarting commands.
Changes apply to the next start/restart; invalid config leaves the last valid
configuration in place. Removed running commands remain visible and stoppable.
Use `Tab` to switch panes and `Enter` to focus Logs. `/` searches in focused
Logs; `G` resumes following. Press `?` for pane-specific keybindings and `q` to
quit without stopping commands. On small screens, inactive panes collapse to clickable
headers; narrow screens keep Logs/Details below the list accordion
(minimum 40 columns × 10 rows).

Stop sends process-group `SIGTERM` only—never an automatic force-kill.
Commands are not guaranteed to survive logout, reboot, or supervisor failure.

For scripting, use `lazyrun --state`, `--start ALIAS`, `--stop ALIAS`,
`--restart ALIAS`, or `--logs ALIAS`. See `lazyrun --help` for options.

## Releasing

Maintainers: see [the release procedure](RELEASE.md) for tagging, gated draft
releases, verification, and publishing.

## License

[MIT](LICENSE).
