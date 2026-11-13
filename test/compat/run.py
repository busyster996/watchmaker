#!/usr/bin/env python3
"""Live-process clock compatibility probes; requires Linux and ptrace permission."""
import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import platform
import queue
import shutil
import subprocess
import tempfile
import threading
import time
import traceback

ROOT = pathlib.Path(__file__).resolve().parents[2]
SOURCE = pathlib.Path(__file__).resolve().parent
BIN_DIR = ROOT / "test/bin"
YEAR = 365 * 86400
FUTURE = 2208988800  # 2040-01-01T00:00:00Z
PAST = 1041379200    # 2003-01-01T00:00:00Z
CASES = [
    ("forward_120s", "+120s", "offset", 120),
    ("backward_120s", "-120s", "offset", -120),
    ("absolute_2003", "2003-01-01 00:00:00", "absolute", PAST),
    ("absolute_2040", "2040-01-01 00:00:00", "absolute", FUTURE),
    ("backward_23y", "-23y", "offset", -23 * YEAR),
    ("forward_23y", "+23y", "offset", 23 * YEAR),
    ("timestamp_seconds", str(FUTURE), "absolute", FUTURE),
    ("absolute_fraction", "2040-01-01 00:00:00.999999999", "absolute", FUTURE + 0.999999999),
    ("timestamp_nanoseconds", str(FUTURE * 10**9 + 999999999), "absolute", FUTURE + 0.999999999),
    ("reset", "0", "offset", 0),
]


def execute(args, *, timeout=60, env=None, cwd=None):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout, env=env, cwd=cwd)
    if result.returncode:
        raise RuntimeError(f"command failed ({result.returncode}): {args}\n{result.stdout}\n{result.stderr}")
    return result.stdout + result.stderr


def build_variants(output, selected):
    BIN_DIR.mkdir(parents=True, exist_ok=True)
    build = pathlib.Path(tempfile.mkdtemp(prefix="compat-build-", dir=BIN_DIR))
    variants, skipped = {}, {}
    jobs = {
        "c-glibc": ("gcc", ["-std=c11", "-O2", "-pthread", str(SOURCE / "native.c")]),
        "c-static": ("gcc", ["-std=c11", "-O2", "-pthread", "-static", str(SOURCE / "native.c")]),
        "c-musl": ("musl-gcc", ["-std=c11", "-O2", "-pthread", "-static", str(SOURCE / "native.c")]),
        "cpp": ("g++", ["-x", "c++", "-std=c++17", "-O2", "-pthread", str(SOURCE / "native.c")]),
        "cpp-static": ("g++", ["-x", "c++", "-std=c++17", "-O2", "-pthread", "-static", str(SOURCE / "native.c")]),
        "rust": ("rustc", ["-O", str(SOURCE / "rust.rs")]),
    }
    names = list(jobs) + ["go", "go-cgo", "java", "python", "node", "dotnet"]
    for name in names:
        if selected and name not in selected:
            continue
        try:
            executable = build / name
            if name in jobs:
                tool, flags = jobs[name]
                if not shutil.which(tool):
                    skipped[name] = f"{tool} unavailable"
                    continue
                log = execute([tool, *flags, "-o", str(executable)])
                command = [str(executable)]
            elif name in ("go", "go-cgo"):
                if not shutil.which("go"):
                    skipped[name] = "go unavailable"
                    continue
                env = dict(os.environ, CGO_ENABLED="1" if name == "go-cgo" else "0")
                files = [str(SOURCE / "go/main.go")]
                if name == "go-cgo":
                    files.append(str(SOURCE / "go/cgo.go"))
                log = execute(["go", "build", "-o", str(executable), *files], env=env, cwd=ROOT)
                command = [str(executable)]
            elif name == "java":
                if not shutil.which("java") or not shutil.which("javac"):
                    skipped[name] = "JDK unavailable"
                    continue
                log = execute(["javac", "-d", str(build), str(SOURCE / "ClockProbe.java")])
                command = ["java", "-Duser.timezone=UTC", "-cp", str(build), "ClockProbe"]
            elif name == "python":
                command, log = ["python3", "-u", str(SOURCE / "python.py")], ""
            elif name == "node":
                if not shutil.which("node"):
                    skipped[name] = "node unavailable"
                    continue
                command, log = ["node", str(SOURCE / "node.cjs")], ""
            elif name == "dotnet":
                if not shutil.which("dotnet"):
                    skipped[name] = "dotnet unavailable"
                    continue
                sdks = execute(["dotnet", "--list-sdks"]).splitlines()
                if not sdks:
                    skipped[name] = "dotnet SDK unavailable"
                    continue
                major = sdks[-1].split(".", 1)[0]
                project = build / "dotnet-src"
                project.mkdir()
                shutil.copyfile(SOURCE / "dotnet.cs", project / "Program.cs")
                (project / "ClockProbe.csproj").write_text(
                    f'<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType>'
                    f'<TargetFramework>net{major}.0</TargetFramework><Nullable>enable</Nullable>'
                    '</PropertyGroup></Project>')
                env = dict(os.environ, DOTNET_CLI_TELEMETRY_OPTOUT="1", DOTNET_SKIP_FIRST_TIME_EXPERIENCE="1")
                log = execute(["dotnet", "build", str(project), "--nologo", "-o", str(build / "dotnet-out")], env=env)
                command = ["dotnet", str(build / "dotnet-out/ClockProbe.dll")]
            (output / f"{name}.build.log").write_text(log)
            variants[name] = command
            print(f"built {name}", flush=True)
        except Exception as error:
            skipped[name] = f"build failed: {error}"
            print(f"build failed {name}: {error}", flush=True)
    return variants, skipped


class Probe:
    def __init__(self, name, command, output):
        self.stderr = (output / f"{name}.stderr.log").open("w")
        self.stdout_log = (output / f"{name}.stdout.log").open("w")
        env = dict(os.environ, TZ="UTC", GOMAXPROCS="4")
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.stderr, text=True, bufsize=1, env=env)
        self.lines = queue.Queue()
        self.pump = threading.Thread(target=self._read, daemon=True)
        self.pump.start()
        try:
            ready = self.line(20)
            if ready != "READY":
                raise RuntimeError(f"probe did not become ready: {ready}")
        except Exception:
            self.close()
            raise

    def _read(self):
        try:
            for line in self.process.stdout:
                self.stdout_log.write(line)
                self.stdout_log.flush()
                self.lines.put(line.rstrip("\n"))
        finally:
            self.lines.put(None)

    def line(self, timeout=5):
        try:
            line = self.lines.get(timeout=timeout)
        except queue.Empty:
            raise TimeoutError(f"pid {self.process.pid}: probe response timed out") from None
        if line is None:
            raise RuntimeError(f"pid {self.process.pid}: probe exited ({self.process.poll()})")
        return line

    def request(self, command):
        before_wall, before_mono = time.time(), time.monotonic()
        self.process.stdin.write(command + "\n")
        self.process.stdin.flush()
        response = json.loads(self.line())
        return {"data": response, "wall_before": before_wall, "wall_after": time.time(),
                "mono_before": before_mono, "mono_after": time.monotonic()}

    def close(self):
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait(timeout=5)
        self.pump.join(timeout=2)
        self.process.stdin.close()
        self.process.stdout.close()
        self.stderr.close()
        self.stdout_log.close()


def process_state(pid):
    result = {"threads": [], "anonymous_rwx_bytes": 0}
    for task in pathlib.Path(f"/proc/{pid}/task").glob("*"):
        try:
            lines = (task / "status").read_text().splitlines()
            values = dict(line.split(":", 1) for line in lines if ":" in line)
            result["threads"].append({"tid": int(task.name), "state": values["State"].strip(),
                                      "tracer": int(values["TracerPid"])})
        except (FileNotFoundError, ProcessLookupError):
            pass
    for line in pathlib.Path(f"/proc/{pid}/maps").read_text().splitlines():
        columns = line.split()
        if columns[1] == "rwxp" and len(columns) == 5:
            start, end = [int(value, 16) for value in columns[0].split("-")]
            result["anonymous_rwx_bytes"] += end - start
    return result


def validate_sample(record, bounds, initial):
    data = record["data"]
    failures = {}
    lo = record["wall_before"] + bounds[0]
    hi = record["wall_after"] + bounds[1]
    for api, value in data["wall"].items():
        tolerance = 1.1 if api == "time" else .03
        if not lo - tolerance <= value <= hi + tolerance:
            failures[api] = {"observed": value, "expected_min": lo, "expected_max": hi,
                             "observed_skew": value - (record["wall_before"] + record["wall_after"]) / 2}
    for api, value in data.get("unskewed_wall", {}).items():
        if not record["wall_before"] - .03 <= value <= record["wall_after"] + .03:
            failures[api] = {"observed": value, "expected": "host clock; bypass or excluded clock id"}
    mono = data.get("mono", data.get("uptime"))
    initial_mono = initial["data"].get("mono", initial["data"].get("uptime"))
    expected_elapsed = record["mono_after"] - initial["mono_after"]
    mono_error = mono - initial_mono - expected_elapsed
    cpu_delta = data.get("cpu", 0) - initial["data"].get("cpu", 0)
    cpu_ok = -.01 <= cpu_delta <= (expected_elapsed + .1) * (os.cpu_count() or 1)
    return {"read_failures": failures, "monotonic_ok": abs(mono_error) < .15, "monotonic_error": mono_error,
            "cpu_clock_ok": cpu_ok, "latency": record["mono_after"] - record["mono_before"]}


def test_variant(name, command, output, watchmaker, stress_rounds):
    try:
        probe = Probe(name, command, output)
    except Exception as error:
        return {"command": command, "cases": [], "error": str(error)}
    result = {"pid": probe.process.pid, "command": command, "cases": []}
    try:
        time.sleep(.25)
        initial = probe.request("sample")
        result["initial"] = initial
        result["initial_state"] = process_state(probe.process.pid)
        result["baseline_validation"] = validate_sample(initial, (0, 0), initial)
        result["baseline_timers"] = probe.request("timers")
        for label, value, kind, expected in CASES:
            start_wall = time.time()
            start = time.monotonic()
            injection = subprocess.run([watchmaker, "--pid", str(probe.process.pid), "--faketime", value],
                                       capture_output=True, text=True, timeout=8)
            end_wall = time.time()
            log_path = output / f"{name}.{label}.inject.log"
            log_path.write_text(injection.stdout + injection.stderr)
            case = {"name": label, "argument": value, "inject_exit": injection.returncode,
                    "inject_seconds": time.monotonic() - start, "log": log_path.name}
            result["cases"].append(case)
            if injection.returncode:
                print(f"{name} {label}: INJECTION FAILED", flush=True)
                continue
            bounds = (expected - end_wall, expected - start_wall) if kind == "absolute" else (expected, expected)
            first = probe.request("sample")
            time.sleep(.06)
            second = probe.request("sample")
            case["samples"] = [first, second]
            case["validation"] = [validate_sample(sample, bounds, initial) for sample in case["samples"]]
            case["progress"] = second["data"]["counter"] - first["data"]["counter"]
            case["state"] = process_state(probe.process.pid)
            case["threads_released"] = bool(case["state"]["threads"]) and all(
                not thread["tracer"] and not thread["state"].startswith(("T", "t"))
                for thread in case["state"]["threads"])
            case["timers"] = probe.request("timers")
            case["relative_timers_ok"] = all(.012 <= elapsed <= 1.0
                for timer, elapsed in case["timers"]["data"].get("relative", {}).items() if timer != "dummy")
            case["monotonic_waits_ok"] = all(.035 <= wait["elapsed"] <= 1.0 and wait["timeout"]
                for timer, wait in case["timers"]["data"].get("waits", {}).items()
                if timer in ("pthread_cond_monotonic", "cpp_cond_steady"))
            case["ok"] = all(not check["read_failures"] and check["monotonic_ok"] and check["cpu_clock_ok"] for check in case["validation"]) \
                and case["progress"] > 0 and case["threads_released"] and case["relative_timers_ok"] and case["monotonic_waits_ok"]
            failures = sorted(set(api for check in case["validation"] for api in check["read_failures"]))
            print(f"{name} {label}: {'PASS' if case['ok'] else 'FAIL'} reads={failures} progress={case['progress']}", flush=True)
        result["stress"] = []
        for i in range(stress_rounds):
            expected = 1 if i % 2 else -1
            injection = subprocess.run([watchmaker, "--pid", str(probe.process.pid), "--faketime", f"{expected:+d}s"],
                                       capture_output=True, text=True, timeout=8)
            sample = probe.request("sample")
            check = validate_sample(sample, (expected, expected), initial)
            result["stress"].append({"inject_exit": injection.returncode, "validation": check})
            if injection.returncode or check["read_failures"] or not check["monotonic_ok"]:
                (output / f"{name}.stress-{i}.log").write_text(injection.stdout + injection.stderr)
        result["final_state"] = process_state(probe.process.pid)
        result["rwx_growth_bytes"] = result["final_state"]["anonymous_rwx_bytes"] - result["initial_state"]["anonymous_rwx_bytes"]
    except Exception as error:
        result["error"] = str(error)
        result["traceback"] = traceback.format_exc()
        try:
            result["failure_state"] = process_state(probe.process.pid)
        except (FileNotFoundError, ProcessLookupError):
            pass
        print(f"{name}: ERROR {error}", flush=True)
    finally:
        probe.close()
    return result


def test_boundaries(output, watchmaker):
    probe = Probe("boundaries", ["python3", "-u", str(SOURCE / "boundaries.py")], output)
    result = {}
    try:
        result["before"] = probe.request("sample")
        injection = subprocess.run([watchmaker, "--pid", str(probe.process.pid), "--faketime", "+120s"],
                                   capture_output=True, text=True, timeout=8)
        (output / "boundaries.inject.log").write_text(injection.stdout + injection.stderr)
        result["inject_exit"] = injection.returncode
        result["after"] = probe.request("sample")
        result["fork"] = probe.request("fork")
        result["exec"] = probe.request("exec")
        record = result["after"]
        result["existing_children_ok"] = all(record["wall_before"] + 119.97 <= child["time"] <= record["wall_after"] + 120.03
                                               for child in record["data"].values())
        result["fork_inherits"] = abs(result["fork"]["data"]["time"] - result["fork"]["wall_after"] - 120) < .03
        result["exec_resets"] = abs(result["exec"]["data"]["time"] - result["exec"]["wall_after"]) < .03
        result["ok"] = not injection.returncode and all(result[key] for key in ("existing_children_ok", "fork_inherits", "exec_resets"))
    finally:
        probe.close()
    return result


def test_coarse(output, watchmaker, command):
    probe = Probe("coarse", command, output)
    try:
        initial = probe.request("sample")
        injection = subprocess.run([watchmaker, "--pid", str(probe.process.pid), "--faketime", "+120s",
                                    "--clockids", "CLOCK_REALTIME,CLOCK_REALTIME_COARSE"],
                                   capture_output=True, text=True, timeout=8)
        (output / "coarse.inject.log").write_text(injection.stdout + injection.stderr)
        sample = probe.request("sample")
        validation_sample = json.loads(json.dumps(sample))
        coarse = validation_sample["data"]["unskewed_wall"].pop("realtime_coarse")
        validation_sample["data"]["wall"]["realtime_coarse"] = coarse
        validation = validate_sample(validation_sample, (120, 120), initial)
        return {"inject_exit": injection.returncode, "sample": sample, "validation": validation,
                "ok": not injection.returncode and not validation["read_failures"] and validation["monotonic_ok"]}
    finally:
        probe.close()


def write_report(output, report):
    (output / "results.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))
    lines = ["# Watchmaker runtime compatibility", "", f"Platform: `{report['platform']}`", "",
             "| Variant | Cases passed | Repeated injections | RWX growth | Errors |",
             "| --- | ---: | ---: | ---: | --- |"]
    for name, result in report["runtimes"].items():
        cases = result.get("cases", [])
        stress = result.get("stress", [])
        passed = sum(case.get("ok", False) for case in cases)
        repeated = sum(not row["inject_exit"] and not row["validation"]["read_failures"]
                       and row["validation"]["monotonic_ok"] for row in stress)
        error = result.get("error", "")
        lines.append(f"| {name} | {passed}/{len(cases)} | {repeated}/{len(stress)} | {result.get('rwx_growth_bytes', 0)} B | {error} |")
    lines.extend(["", "Raw per-API readings, waits, process/thread states and injection logs are in `results.json` and the adjacent logs.", ""])
    if report.get("boundaries"):
        lines.append(f"Fork/exec and named-child checks: {report['boundaries'].get('ok', False)}")
    if report.get("coarse"):
        lines.append(f"Explicit CLOCK_REALTIME_COARSE selection: {report['coarse'].get('ok', False)}")
    if report["skipped"]:
        lines.extend(["## Unavailable/build failures", "", "```json", json.dumps(report["skipped"], indent=2), "```", ""])
    (output / "report.md").write_text("\n".join(lines))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--watchmaker", default=str(ROOT / "bin/watchmaker_linux_amd64"))
    parser.add_argument("--output", type=pathlib.Path)
    parser.add_argument("--runtimes", help="comma-separated variant names")
    parser.add_argument("--stress-rounds", type=int, default=10)
    args = parser.parse_args()
    BIN_DIR.mkdir(parents=True, exist_ok=True)
    output = args.output or pathlib.Path(tempfile.mkdtemp(prefix="compat-results-", dir=BIN_DIR))
    output.mkdir(parents=True, exist_ok=True)
    print(f"artifacts: {output}", flush=True)
    selected = set(args.runtimes.split(",")) if args.runtimes else None
    variants, skipped = build_variants(output, selected)
    versions = {}
    for name, command in {
        "gcc": ["gcc", "--version"], "g++": ["g++", "--version"], "go": ["go", "version"],
        "java": ["java", "-version"], "python": ["python3", "--version"], "node": ["node", "--version"],
        "rust": ["rustc", "--version"], "dotnet": ["dotnet", "--list-runtimes"],
        "musl": ["/lib/ld-musl-x86_64.so.1"],
    }.items():
        if shutil.which(command[0]):
            version = subprocess.run(command, capture_output=True, text=True, timeout=15)
            versions[name] = (version.stdout + version.stderr).strip()
    report = {"timestamp_utc": dt.datetime.now(dt.timezone.utc).isoformat(), "platform": platform.platform(),
              "commit": execute(["git", "rev-parse", "HEAD"], cwd=ROOT).strip(), "versions": versions,
              "working_tree": execute(["git", "status", "--porcelain"], cwd=ROOT),
              "watchmaker_sha256": hashlib.sha256(pathlib.Path(shutil.which(args.watchmaker) or args.watchmaker).read_bytes()).hexdigest(),
              "runtimes": {}, "skipped": skipped}
    for name, command in variants.items():
        report["runtimes"][name] = test_variant(name, command, output, args.watchmaker, args.stress_rounds)
        write_report(output, report)
    for name, check in [("boundaries", lambda: test_boundaries(output, args.watchmaker)),
                        ("coarse", lambda: test_coarse(output, args.watchmaker, variants["c-glibc"]))]:
        if name == "coarse" and "c-glibc" not in variants:
            continue
        try:
            report[name] = check()
        except Exception as error:
            report[name] = {"ok": False, "error": str(error)}
        print(f"{name}: {'PASS' if report[name]['ok'] else 'FAIL'}", flush=True)
    write_report(output, report)
    print((output / "report.md").read_text(), flush=True)
    failed = any(result.get("error") or not result["cases"] or any(not case.get("ok") for case in result["cases"])
                 or any(row["inject_exit"] or row["validation"]["read_failures"] or not row["validation"]["monotonic_ok"] for row in result.get("stress", []))
                 for result in report["runtimes"].values())
    extra_failed = any(not report[name]["ok"] for name in ("boundaries", "coarse") if name in report)
    return 1 if failed or extra_failed or any("build failed" in reason for reason in skipped.values()) else 0


if __name__ == "__main__":
    raise SystemExit(main())
