#!/usr/bin/env python3
"""Stress CLI injection into busy native C++ threads, locks and signal handlers."""
import argparse
import hashlib
import json
import pathlib
import platform
import shutil
import subprocess
import sys
import tempfile
import time

# Keep imports from producing build artifacts alongside the probe sources.
sys.dont_write_bytecode = True
import run as compat

DAY = 86400
SWITCHES = [("-415d", -415 * DAY), ("0", 0), ("+1d", DAY), ("0", 0),
            ("-400d", -400 * DAY), ("0", 0), ("+120s", 120), ("-120s", -120), ("0", 0)]


def sample(probe, offset):
    record = probe.request("sample")
    lo, hi = record["wall_before"] + offset - .05, record["wall_after"] + offset + .05
    if not lo <= record["data"]["wall"] <= hi:
        raise AssertionError(f"wrong C++ wall time: {record}, expected offset {offset}")
    return record


def progress(probe, offset, signals):
    first = sample(probe, offset)
    deadline = time.monotonic() + 1
    while True:
        time.sleep(.035)
        second = sample(probe, offset)
        a, b = first["data"], second["data"]
        counts = [end - start for start, end in zip(a["counts"], b["counts"])]
        if min(counts) > 0 and b["ticks"] > a["ticks"] and (not signals or b["signals"] > a["signals"]):
            return {"first": first, "second": second, "progress": counts}
        if time.monotonic() >= deadline:
            raise AssertionError(f"C++ thread, timer or signal stalled: {first}, {second}")


def diagnose(probe, label, output):
    data = {"target_exit": probe.process.poll()}
    if data["target_exit"] is not None:
        return data
    try:
        data["state"] = compat.process_state(probe.process.pid)
    except Exception as error:
        data["state_error"] = str(error)
    if shutil.which("gdb"):
        try:
            trace = subprocess.run(["gdb", "-q", "-n", "-batch", "-p", str(probe.process.pid),
                                    "-ex", "set pagination off", "-ex", "thread apply all bt",
                                    "-ex", "info registers", "-ex", "detach"],
                                   capture_output=True, text=True, timeout=10)
            (output / f"{label}.gdb.log").write_text(trace.stdout + trace.stderr)
        except Exception as error:
            data["gdb_error"] = str(error)
    return data


def session(executable, watchmaker, mode, label, offsets, output):
    record = {"mode": mode, "label": label, "steps": [], "ok": False}
    probe = None
    try:
        probe = compat.Probe(label, [str(executable), mode], output)
        progress(probe, 0, mode == "signals")
        for index, (argument, offset) in enumerate(offsets):
            step = {"argument": argument, "ok": False}
            record["steps"].append(step)
            start = time.monotonic()
            try:
                injection = subprocess.run([watchmaker, "--pid", str(probe.process.pid), "--faketime", argument],
                                           capture_output=True, text=True, timeout=3)
            except subprocess.TimeoutExpired as error:
                (output / f"{label}.{index}.inject.log").write_text(
                    (error.stdout or b"").decode(errors="replace") + (error.stderr or b"").decode(errors="replace"))
                raise TimeoutError("CLI injection exceeded three seconds") from error
            step["inject_exit"] = injection.returncode
            step["inject_elapsed"] = time.monotonic() - start
            (output / f"{label}.{index}.inject.log").write_text(injection.stdout + injection.stderr)
            if injection.returncode:
                raise AssertionError(f"CLI injection failed: {injection.returncode}")
            step.update(progress(probe, offset, mode == "signals"))
            step["state"] = compat.process_state(probe.process.pid)
            if not step["state"]["threads"] or any(thread["tracer"] or thread["state"].startswith(("T", "t"))
                                                    for thread in step["state"]["threads"]):
                raise AssertionError(f"thread still traced or stopped: {step['state']}")
            step["ok"] = True
        record["ok"] = True
    except Exception as error:
        record["error"] = repr(error)
        if probe:
            record["diagnostics"] = diagnose(probe, label, output)
    finally:
        if probe:
            probe.close()
    print(f"{label}: {'PASS' if record['ok'] else 'FAIL'} {len(record['steps'])}/{len(offsets)}"
          + (f" {record['error']}" if record.get("error") else ""), flush=True)
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--watchmaker", default=str(compat.ROOT / "bin/watchmaker_linux_amd64"))
    parser.add_argument("--output", type=pathlib.Path)
    args = parser.parse_args()
    compat.BIN_DIR.mkdir(parents=True, exist_ok=True)
    output = args.output or pathlib.Path(tempfile.mkdtemp(prefix="cpp-stress-results-", dir=compat.BIN_DIR))
    output.mkdir(parents=True, exist_ok=True)
    build = pathlib.Path(tempfile.mkdtemp(prefix="cpp-stress-build-", dir=compat.BIN_DIR))
    executable = build / "cpp_signals"
    log = compat.execute(["g++", "-std=c++17", "-g", "-O2", "-pthread", "-Wall", "-Wextra", "-o", str(executable),
                          str(compat.ROOT / "test/test_cpp_signals.cpp")])
    (output / "build.log").write_text(log)
    report = {"platform": platform.platform(), "watchmaker": args.watchmaker,
              "watchmaker_sha256": hashlib.sha256(pathlib.Path(shutil.which(args.watchmaker) or args.watchmaker).read_bytes()).hexdigest(),
              "commit": compat.execute(["git", "rev-parse", "HEAD"], cwd=compat.ROOT).strip(),
              "working_tree": compat.execute(["git", "status", "--porcelain"], cwd=compat.ROOT), "sessions": []}
    for mode in ("clock", "mutex", "signals"):
        cases = [(f"{mode}-fresh-{i}", [("0" if i % 2 else "+0s", 0)]) for i in range(8)]
        cases += [(f"{mode}-repeated-zero", [("+0s", 0)] * 60), (f"{mode}-switch", SWITCHES * 3)]
        for label, offsets in cases:
            record = session(executable, args.watchmaker, mode, label, offsets, output)
            report["sessions"].append(record)
            (output / "results.json").write_text(json.dumps(report, indent=2))
    passed = sum(record["ok"] for record in report["sessions"])
    injections = sum(len(record["steps"]) for record in report["sessions"])
    print(f"{passed}/{len(report['sessions'])} sessions; {injections} injections; artifacts: {output}", flush=True)
    return 0 if passed == len(report["sessions"]) else 1


if __name__ == "__main__":
    raise SystemExit(main())
