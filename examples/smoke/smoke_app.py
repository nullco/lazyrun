"""Real, local-only Flask reloader / Celery prefork release fixtures.

The filesystem broker is for this smoke test, not a production broker example.
All addresses are loopback; the debug HTTP server has no interactive debugger.
"""
import argparse
import json
import os
from pathlib import Path
import time

from celery import Celery
from flask import Flask, jsonify

root = Path(os.environ.get("SMOKE_ROOT", ".")).resolve()
broker = root / ".smoke-broker"
for name in ("messages", "processed", "control"):
    (broker / name).mkdir(parents=True, exist_ok=True)

celery = Celery("lazyrun-smoke", broker="filesystem://")
celery.conf.update(
    broker_transport_options={
        "data_folder_in": str(broker / "messages"),
        "data_folder_out": str(broker / "messages"),
        "processed_folder": str(broker / "processed"),
        "control_folder": str(broker / "control"),
        "polling_interval": 0.1,
    },
    task_default_queue="lazyrun-smoke",
    worker_prefetch_multiplier=1,
    task_ignore_result=True,
    task_serializer="json",
    accept_content=["json"],
)

app = Flask(__name__)


@app.get("/health")
def health():
    return jsonify(pid=os.getpid(), pgid=os.getpgrp(), marker="lazyrun-smoke")


@celery.task(name="lazyrun.smoke.slow_job")
def slow_job(seconds, token):
    identity = {"pid": os.getpid(), "pgid": os.getpgrp(), "token": token}
    (root / f"started-{token}.json").write_text(json.dumps(identity))
    print(f"smoke task {token} started", flush=True)
    time.sleep(seconds)
    (root / f"done-{token}.json").write_text(json.dumps(identity))
    print(f"smoke task {token} finished", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("serve", "enqueue", "wait-idle"))
    parser.add_argument("--seconds", type=float, default=1)
    parser.add_argument("--token", default="manual")
    args = parser.parse_args()
    if args.action == "serve":
        app.run(
            host="127.0.0.1",
            port=int(os.environ.get("SMOKE_PORT", "0")),
            debug=True,
            use_debugger=False,
            use_reloader=True,
            reloader_type="stat",
        )
    elif args.action == "wait-idle":
        # A child's done file precedes its result reaching the parent. Use real
        # remote control to fence parent-side completion before an idle stop.
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            active = celery.control.inspect(timeout=1).active()
            if active and all(not tasks for tasks in active.values()):
                print("worker idle", flush=True)
                break
            time.sleep(0.1)
        else:
            raise SystemExit("worker did not acknowledge an idle pool")
    else:
        result = slow_job.delay(args.seconds, args.token)
        print(f"queued {result.id}", flush=True)
