# lazyrun

A terminal dashboard for your project's services and tasks. Linux only.
Aims to offer a familiar UX inspired by [lazydocker](https://github.com/jesseduffield/lazydocker)
and [lazygit](https://github.com/jesseduffield/lazygit).

## Features

- Start, stop, and restart named commands from one dashboard.
- Commands keep running when you close the dashboard; reconnect anytime.
- Wrapped, colored logs with live follow, history paging, and retained-log search.
- Keyboard and mouse navigation, plus a headless CLI.
- Per-project configuration in `lazyrun.yml`. Nothing starts automatically.

## Quick start

Requires Linux and Go 1.25.10 or newer.

### 1. Build

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
Use `Tab` to switch panes and `Enter` to focus Logs. `/` searches in focused
Logs; `G` resumes following. Press `?` for help and `q` to quit the dashboard
without stopping commands.

Stop sends process-group `SIGTERM` only—never an automatic force-kill.
Commands are not guaranteed to survive logout, reboot, or supervisor failure.

For scripting, use `lazyrun --state`, `--start ALIAS`, `--stop ALIAS`,
`--restart ALIAS`, or `--logs ALIAS`. See `lazyrun --help` for options.

## License

[MIT](LICENSE).
