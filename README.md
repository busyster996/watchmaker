# watchmaker

injector which change the time inside the process instead of the host.

## Install

Requires Linux amd64/arm64, kernel 3.11 or newer, and permission to ptrace the target.

```shell
wget https://github.com/busybox-org/watchmaker/releases/download/v0.0.2/watchmaker_linux_amd64 -O /bin/watchmaker
chmod +x /bin/watchmaker
```

## Usage
```shell
# absolute dates is "YYYY-MM-DD hh:mm:ss"
watchmaker --pid 1536 --faketime "2003-01-01 10:00:05"

# absolute dates is timestamp (seconds or nanosecond)
watchmaker --pid 1536 --faketime 1705022260

# time offset(no units), default is seconds 
watchmaker --pid 1536 --faketime +120 
# or
watchmaker --pid 1536 --faketime -120

# time offset(s)
watchmaker --pid 1536 --faketime +120s
# or
watchmaker --pid 1536 --faketime -120s

# time offset(m)
watchmaker --pid 1536 --faketime +12m
# or
watchmaker --pid 1536 --faketime -12m

# time offset(h)
watchmaker --pid 1536 --faketime +12h
# or
watchmaker --pid 1536 --faketime -12h

# time offset(d)
watchmaker --pid 1536 --faketime +12d
# or
watchmaker --pid 1536 --faketime -12d

# time offset(y)
watchmaker --pid 1536 --faketime +1y
# or
watchmaker --pid 1536 --faketime -1y
```

## Timers and waits

Watchmaker changes the time returned by the process's vDSO functions. The
kernel clock used by absolute deadlines is unchanged. For example,
`cv.wait_until(lock, std::chrono::system_clock::now() + 50ms)` can wait an
extra 120 seconds after applying `--faketime +120s`. Moving time backwards
can instead make kernel waits return immediately and cause busy retries.
The same mismatch affects glibc's `pthread_cond_timedwait` when it uses
`CLOCK_REALTIME`.

Use `std::chrono::steady_clock` for elapsed time and timeout deadlines. Use
relative waits backed by a monotonic clock; older C++ libraries may implement
`wait_for` using realtime deadlines, so verify the target runtime. With pthread
condition variables, configure `pthread_condattr_setclock` to use
`CLOCK_MONOTONIC`. Leave monotonic clocks out of `--clockids` when changing
calendar time. The default clock selection does this.

Java's `LockSupport.parkUntil` also uses an absolute wall-clock deadline and
exhibits this mismatch. Relative waits such as `parkNanos` and monotonic
deadlines should be checked separately from wall-clock reads.

## Runtime compatibility tests

The [compatibility report](test/compat/REPORT.zh-CN.md) covers C/C++, Go,
Java, Python, Node.js, Rust and .NET on Linux amd64, including static linking,
musl, cgo, repeated injection and timer behavior.

```shell
make test_compat
```

Test binaries, probe builds and generated results are placed under `test/bin`.
See the [test instructions](test/compat/README.md) for individual runtime selection.

## Reference

This project uses the following open-source software:

* [Chaos-mesh](https://github.com/chaos-mesh/chaos-mesh) - Reference chaos-mesh's watchmaker component to simulate process time
* [Libfaketime](https://github.com/wolfcw/libfaketime) - Reference the libfaketime dynamic link library to simulate time
* [timechaos-our-final-hack](https://chaos-mesh.org/blog/simulating-clock-skew-in-k8s-without-affecting-other-containers-on-node/#timechaos-our-final-hack) - Reference the blog to simulate time
