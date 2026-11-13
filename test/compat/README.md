# Runtime compatibility probes

These probes modify running processes and compare the returned wall time with
the controller's unchanged host clock. They also check background thread
progress, monotonic time, relative timers and selected absolute waits. The
[2026-11-13 report](REPORT.zh-CN.md) describes the measured results and limitations.

## Run

From the repository root:

```shell
make test_compat
```

This builds the native watchmaker binary, then tests every available runtime.
The fixtures require the corresponding compilers/interpreters: GCC, G++,
musl-gcc, Go, a JDK, Python 3, Node.js, Rust and/or a .NET SDK. Missing tools
are recorded as skipped; build errors and failed assertions return a nonzero
exit status. Linux ptrace permissions must allow tracing the child processes.

To select runtimes or specify the report directory:

```shell
python3 test/compat/run.py \
  --runtimes c-glibc,cpp,go,java,python \
  --stress-rounds 20 \
  --output test/bin/compat-selected
```

Available variant names are `c-glibc`, `c-static`, `c-musl`, `cpp`, `cpp-static`,
`go`, `go-cgo`, `java`, `python`, `node`, `rust`, and `dotnet`.
Use `--watchmaker /absolute/path/to/watchmaker` to test another binary.
The checked-in default selects `bin/watchmaker_linux_amd64`; runtime validation
has been performed on amd64, not arm64.

For the native C++ hang investigation, run the busy-main-thread stress probe:

```shell
python3 test/compat/cpp_stress.py --output test/bin/cpp-stress-final
```

This performs 285 CLI injections across clock readers, contended mutexes and
1 ms SIGALRM delivery. It covers fresh zero-offset injection, 60 consecutive
zero-offset injections, and repeated forward/backward/reset sequences. Each
step checks wall time, main/worker/timer/signal progress and ptrace detachment.
Failures save process state and a gdb backtrace when gdb is available.
`make test` also runs deterministic pending-signal and interrupted-mmap
regressions, including restoration of registers, code and the original signal mask.

## Outputs

Compiled probes always go into a unique `test/bin/compat-build-*` directory,
including Java classes and .NET project outputs. Default results go into
`test/bin/compat-results-*`; `--output` changes the results directory only.
Each results directory contains:

- `results.json`: per-API samples, expected time bounds, timer durations,
  injection status, process/thread state and memory growth.
- `report.md`: a compact summary.
- `*.inject.log`, `*.stdout.log`, `*.stderr.log`, `*.build.log`: diagnostic evidence.

Regular C tests build into `test/bin/test_*`. The Go-driven C++ regression
builds into `test/bin/cpp-*` and `test/bin/cpp-signals-*`. The CLI stress probe
builds into `test/bin/cpp-stress-build-*`. Existing `bin` ignore rules exclude these
generated files from Git. `make -C test clean` removes `test/bin`.

## Interpretation

Each runtime receives ten input cases and then repeated alternating offsets.
The cases include positive/negative seconds, past/future absolute dates,
positive/negative 23 years, seconds/nanoseconds timestamps, fractional dates,
and reset to the host clock. Each process has four background workers that
read time throughout injection. Wall-clock assertions allow for interface
precision and the injection/sampling interval.

Passing the normal cases means the sampled wall APIs change correctly while
monotonic time, relative waits and thread progress remain usable. It does not
mean every timeout API supports virtual wall time. Absolute realtime waits
are recorded separately and rescued after about 100 ms, so an incompatible
deadline cannot leave the test process waiting for years. Direct time
syscalls, cached clock origins and clocks excluded from `--clockids` are
expected to retain host time.

The suite also verifies existing descendants, names containing spaces,
post-injection fork/exec behavior and explicit `CLOCK_REALTIME_COARSE`
selection. It never adjusts the host clock.
