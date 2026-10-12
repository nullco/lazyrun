# lazyrun

A terminal dashboard for your project's services and tasks. Linux only.
Inspired by [lazydocker](https://github.com/jesseduffield/lazydocker)
and [lazygit](https://github.com/jesseduffield/lazygit).

- Start, stop, and restart commands from one dashboard.
- Close the dashboard and reconnect without stopping your commands.
- Browse and search colored logs.
- Switch between projects, with keyboard or mouse navigation.
- Configure commands in `lazyrun.yml`. Nothing starts automatically.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/nullco/lazyrun/main/scripts/install.sh | sh
```

Requires `curl`. Installs to `~/.local/bin`; make sure that directory is on your
`PATH`. Rerun the same command to upgrade.

You can [review the installer](https://github.com/nullco/lazyrun/blob/main/scripts/install.sh)
before running it, or download a binary from [GitHub Releases](https://github.com/nullco/lazyrun/releases).

<details>
<summary>Build from source</summary>

Requires Go 1.25.10 or newer.

```sh
git clone https://github.com/nullco/lazyrun.git
cd lazyrun
make build
export PATH="$PWD/bin:$PATH"
```

</details>

## Quick start

Create `lazyrun.yml` in your project with your own commands:

```yaml
version: 1
services:
  api:
    command: python3 -u -m http.server 8000
    # restart_timeout: 60s
tasks:
  tests:
    command: go test ./...
```

Restart waits up to 30 seconds for a command to stop. Uncomment `restart_timeout`
if it needs longer; use a positive duration such as `60s` or `2m`. If shutdown
times out, the restart is canceled. Stop never force-kills a command.

Commands run from the project directory and inherit the shell environment you
launched lazyrun from.

Then open the dashboard:

```sh
cd /path/to/project
lazyrun --check
lazyrun
```

The dashboard needs a terminal at least 40 columns wide and 10 rows tall.

## Controls

Use the arrow keys or mouse to select a command.

| Key | Action |
| --- | --- |
| `S` | Start a service or run a task |
| `s` | Stop |
| `r` | Restart or rerun |
| `R` | Reload `lazyrun.yml` without restarting commands |
| `Tab` | Switch panes |
| `Enter` | Open Logs / Details |
| `/` | Search in Logs |
| `G` | Follow the latest logs |
| `?` | Show all keybindings |
| `q` | Quit without stopping commands |

Configuration changes take effect on the next start or restart. Commands may stop
when you log out or reboot.

## Multiple projects

Open `lazyrun` in each project directory to add it to the Projects list. Select a
project with the arrow keys or mouse to view its commands. You can then launch
`lazyrun` from outside a project directory and choose a project from the list.

If a project is missing, open `lazyrun` from its directory again. Switching
projects doesn't load `.env` files or activate virtual environments.

## Scripting

Run these from your project directory:

```sh
lazyrun --state
lazyrun --start api
lazyrun --stop api
lazyrun --restart api
lazyrun --logs api
```

See `lazyrun --help` for more options.

## License

[MIT](LICENSE).
