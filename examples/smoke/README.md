# Real Flask / Celery smoke project

These are local development/release fixtures, not production deployment advice.
The real Flask/Werkzeug stat reloader binds **127.0.0.1** on a dynamically chosen
port (`SMOKE_PORT=0` by default) with the interactive debugger disabled. Celery
uses a real prefork worker and Kombu's local filesystem broker; no Redis/RabbitMQ
or internet-facing service is required. Start nothing automatically.

## Automated release gate

From the repository root, with Linux and Python 3.12 (including `venv`):

```sh
python3 -m venv /tmp/lazyrun-smoke-venv
/tmp/lazyrun-smoke-venv/bin/python -m pip install -r examples/smoke/requirements.txt
LAZYRUN_SMOKE_PYTHON=/tmp/lazyrun-smoke-venv/bin/python make smoke
```

Use your own fresh venv path if that name already exists. `make smoke` fails if
`LAZYRUN_SMOKE_PYTHON` is missing; normal `go test ./...` skips these opt-in tests
and never downloads Python packages. Pin updates require reviewing the observed
shutdown behavior, not merely refreshing a lockfile.

The tests copy this application into isolated temporary projects, build the real
race-instrumented lazyrun executable, and verify:

- Opening starts nothing; Flask has an actual reloader child in the managed group.
- HTTP remains available while no client is connected; reconnect preserves run/PID.
- Editing the module changes the Werkzeug child PID, not the managed run/group.
- Explicit restart replaces only a fully exited group; stop reaps all ordinary
  reloader/prefork descendants and retains final logs/outcome.
- Real Celery tasks execute inside prefork children. A real `inspect active`
  acknowledgement fences parent-side completion before the idle-pool stop; a
  child's done marker alone is too early (the parent may still await its result).
- A busy threads-pool worker finishes its task on SIGTERM; a restart exceeding
  lazyrun's three-second wait is canceled, never force-killed or started later.
- A busy default prefork worker receives SIGTERM in its children, reports their
  loss, and can abort its task despite printing `Warm shutdown` in the parent.

## Important Celery limitation

lazyrun stops the **entire process group**, not only Celery's parent. Default
prefork children do not ignore SIGTERM in the tested Celery/billiard versions.
**An in-flight prefork task may be interrupted.** A `Warm shutdown` log line is
not proof of task completion. The worker's shutdown can also exceed the restart
wait while it handles child loss; query state rather than repeatedly restarting.

If finishing jobs matters, test your exact pool/worker/task configuration. The
threads-pool fixture demonstrates in-process warm completion, not a guarantee
for every Celery pool or an automatic pool recommendation for CPU-bound jobs.
No application-specific parent-only signaling or automatic SIGKILL is added.
Celery itself can send signals/manage children independently of lazyrun.

## Try the dashboard manually

Use a disposable copy so the source example remains unchanged:

```sh
cp -R examples/smoke /tmp/my-lazyrun-smoke
cd /tmp/my-lazyrun-smoke
python3 -m venv .venv
.venv/bin/python -m pip install -r requirements.txt
/path/to/lazyrun --check
/path/to/lazyrun
```

Select/start `api` and/or `worker` explicitly, then run `enqueue`. Read the Flask
port from its Logs tab. Stop the worker while idle for a clean prefork shutdown,
or deliberately inspect what happens during the five-second `manual` task.
Quitting does not stop services: stop them deliberately before removing the
project/venv/broker files. Files under `.smoke-broker/` and `started-*.json` /
`done-*.json` are fixture application data, not lazyrun's state directory.
