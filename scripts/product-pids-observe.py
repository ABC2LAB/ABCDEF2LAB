#!/usr/bin/env python3
"""Read-only observation inside the actual preparation container.

The observer adds one task to pids.current/peak; engine_threads counts PID 1 only.
Pipe stdout to an operator-owned file outside the container. No files are changed.
"""
import argparse
import json
import time
from pathlib import Path

p = argparse.ArgumentParser()
p.add_argument("--seconds", type=int, default=45)
a = p.parse_args()
cg = Path("/sys/fs/cgroup")
def events():
    return int(dict(line.split() for line in (cg / "pids.events").read_text().splitlines())["max"])
if (cg / "pids.max").read_text().strip() != "256":
    raise RuntimeError("actual_product_pids_limit_256_required")
initial_events = events()
engine_peak = observed_peak = 0
start = time.monotonic()
last = -1
while time.monotonic() - start < a.seconds:
    threads = int(next(line.split()[1] for line in Path("/proc/1/status").read_text().splitlines() if line.startswith("Threads:")))
    current = int((cg / "pids.current").read_text())
    engine_peak, observed_peak = max(engine_peak, threads), max(observed_peak, current)
    second = int(time.monotonic() - start)
    if second != last:
        print(json.dumps(dict(second=second, engine_threads=threads, engine_peak=engine_peak, current=current, observed_peak=observed_peak, cgroup_peak=int((cg / "pids.peak").read_text()), exhaustion_delta=events()-initial_events)), flush=True)
        last = second
    time.sleep(.02)
delta = events() - initial_events
passed = delta == 0 and observed_peak <= 256 and engine_peak <= 64
print(json.dumps(dict(passed=passed, observer_tasks=1, engine_peak=engine_peak, observed_peak=observed_peak, cgroup_peak=int((cg / "pids.peak").read_text()), exhaustion_delta=delta)), flush=True)
if not passed:
    raise RuntimeError("product_pids_acceptance_failed")
