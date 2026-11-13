# 运行时兼容性测试：2026-11-13

本机实测的 C、C++、Go、Java、Python、Node.js、Rust、.NET 常规读时接口均能被修改。
但“日期读数能改”不等于“所有定时等待都兼容”：glibc 的 realtime 绝对等待、
C++ 的 `condition_variable::wait_until(system_clock)`、Java 的 `parkUntil` 仍存在阻塞或提前返回的问题。

首轮源码基于 `4d69626`，包含 `cffb72a` 的耗时循环修复、`c1d9f1e` 的绝对时间偏移方向修复，
以及 `4d69626` 的子进程名解析修复。追加测试基于 `7d167f9`，加上本次 ptrace 信号处理修复。
构建和原始数据保存在 `test/bin`。

## 环境与测试范围

- Linux amd64，WSL2 内核 `6.18.54.1-microsoft-standard-WSL2`，glibc 2.41，musl 1.2.5。
- GCC/G++ 14.2.0；Go 1.27.4；OpenJDK 21.0.13；Python 3.13.5；Node.js 26.8.1。
- Rust `1.100.0-nightly (330d31712)`；.NET Runtime 10.0.14 / SDK 10.0.403。
- 探针统一使用 `TZ=UTC`，以纪元秒值核对修改结果。
- 本机保留的既有模块配置为 Go 1.26.0、`golang.org/x/sys v0.50.0`，未把这两个依赖文件的原有改动提交。
- 运行时实测限于上述版本和 amd64；arm64 只做交叉构建检查。

每个进程带四个后台读时间线程，连续测试 10 种输入，再执行 20 次正负偏移交替注入。
共 12 种构建/运行方式、120 个完整场景、240 次额外重复注入。
核心矩阵总计 360 次 CLI 注入、1,680 次普通 wall-clock 数值断言；另外检查了子进程与 coarse 时钟。

10 种输入为 `+120s`、`-120s`、2003 年绝对日期、2040 年绝对日期、`-23y`、`+23y`、
秒时间戳、带 9 位小数的日期、19 位纳秒时间戳、`0` 归零。
每次同时检查读数、线程是否解除跟踪、多线程计数器是否增长、单调时钟是否连续、相对等待是否正常返回。

## 常规接口结果

“通过”指上面的读数、进度和相对等待检查；绝对等待的例外列在下一节。

| 构建/运行方式 | 主要读时接口 | 完整场景 | 额外重复注入 |
| --- | --- | ---: | ---: |
| C / glibc 动态链接 | `time`、`gettimeofday`、`clock_gettime`、`timespec_get` | 10/10 | 20/20 |
| C / glibc 静态链接 | 同上 | 10/10 | 20/20 |
| C / musl 静态链接 | 同上 | 10/10 | 20/20 |
| C++ / 动态链接 | C 接口、`system_clock`、本机 `high_resolution_clock` | 10/10 | 20/20 |
| C++ / 静态链接 | 同上 | 10/10 | 20/20 |
| Go / CGO_ENABLED=0 | `time.Now`、`time.Now().UTC()` | 10/10 | 20/20 |
| Go / cgo | Go 接口和 C `clock_gettime`、`gettimeofday` | 10/10 | 20/20 |
| Java 21 | `currentTimeMillis`、`Instant.now`、`Date`、`OffsetDateTime.now` | 10/10 | 20/20 |
| Python 3.13 | `time.time`、`time_ns`、`datetime.now`、`clock_gettime` | 10/10 | 20/20 |
| Node.js 26 | `Date.now`、`new Date` | 10/10 | 20/20 |
| Rust | `SystemTime::now` | 10/10 | 20/20 |
| .NET 10 | `DateTime.UtcNow`、`DateTime.Now`、`DateTimeOffset.UtcNow` | 10/10 | 20/20 |

Go 的 Sleep/After/Timer/Ticker，Java 的 sleep/wait/parkNanos/调度器，Python 的 sleep/Event/Condition/
Timer/asyncio，Node.js 的 setTimeout，Rust 的 sleep/Condvar，.NET 的 Sleep/Task.Delay/Semaphore/Monitor
均继续返回。后台工作线程持续有进度，检查到的线程均已解除 ptrace。
采样往返时间包含管道通信和序列化，原始数据逐次记录，不能当作单函数性能基准。

## 绝对时间等待的例外

等待目标为“当前 wall time + 50 ms”，另一个线程在约 100 ms 时主动唤醒，防止探针长期阻塞。

| 接口/实现 | 时间前调 120 秒 | 时间后调 120 秒 | 归零后 |
| --- | --- | --- | --- |
| glibc `pthread_cond_timedwait(CLOCK_REALTIME)`，含静态链接 | 50 ms 未到期，需 100 ms 主动唤醒 | 几微秒就返回 | 约 50 ms |
| libstdc++ `condition_variable::wait_until(system_clock)` | 50 ms 未返回，需 100 ms 主动唤醒 | 几微秒就返回 | 约 50 ms |
| Java `LockSupport.parkUntil` | 50 ms 未返回，需 100 ms `unpark` | 几微秒就返回 | 约 50 ms |
| musl 1.2.5 `pthread_cond_timedwait(CLOCK_REALTIME)` | 本机约 50 ms | 本机约 50 ms | 约 50 ms |
| pthread monotonic / C++ steady-clock 等待 | 约 50 ms | 约 50 ms | 约 50 ms |

原因是 vDSO 返回的时间被修改了，内核用于绝对截止时间的时钟没有修改。glibc 和上述 Java/C++
接口把虚拟日期作为内核绝对截止时间，因而产生偏差；本机 musl 的转换路径没有出现这个问题。
仅凭语言名称不能判断是否会 hang，还要看应用使用的等待接口及 libc/标准库实现。
计时和超时应使用基于 `CLOCK_MONOTONIC` 的等待；旧版 C++ 库的相对等待实现也需单独核对。

## 进程边界与未被修改的路径

- 已有普通子进程，以及进程名含空格的已有子进程：修改成功。
- 注入后再 `fork`：子进程继承修改。
- 注入后再 `exec`：新程序恢复真实时间，需要再次注入。这个行为已实测，不是对子进程自动持续监控。
- 直接 `syscall(SYS_clock_gettime, ...)`：仍为主机时间，绕过 vDSO hook。
- amd64 默认未选中的 `CLOCK_REALTIME_COARSE`：仍为主机时间；显式指定
  `--clockids CLOCK_REALTIME,CLOCK_REALTIME_COARSE` 后已验证生效。
- Node.js 的 `performance.timeOrigin + performance.now()`：仍沿用启动时的时间原点，
  与注入后的 `Date.now()` 不再一致。单调时钟本身保持连续。

## 本轮发现并修复的问题

1. 大偏移逐秒循环，频繁读时间会产生明显假死；由 `cffb72a` 修复。
2. 小数秒较大的绝对日期/纳秒时间戳会算反偏移方向。首轮 C/Python 各有 2/10 场景失败；
   `c1d9f1e` 修复后，完整矩阵全部通过，并增加了过去/未来纳秒参数的单元测试。
3. `/proc/PID/stat` 按空格扫描会漏掉进程名带空格的子进程。实际复现为父进程和普通子进程
   已前调 120 秒，带空格名称的子进程保持真实时间；`4d69626` 修复后，进程边界测试通过。
4. 原先 ptrace 等待逻辑不区分信号暂停和单步完成。原生 C++ 每 1 ms 收到 `SIGALRM` 时，
   首次 `+0s` 即可使注入失败：等待收到 `SIGALRM`，却把尚未执行的 `mmap` 系统调用号 `9`
   当成成功地址 `0x9`，随后写入时报 `bad address`。本次修复在远程系统调用期间暂时屏蔽
   可屏蔽信号，核对停止类型和指令位置，并在成功或错误退出时恢复寄存器、指令和原信号屏蔽设置。
   与初始中断竞争的信号先正常投递；不可屏蔽信号导致失败时，不重试远程调用，解除跟踪时保留信号。
   解除跟踪也会尝试所有已跟踪线程，避免某个线程报错后跳过其余线程。

## 原生 C++ 挂起排查与 issue #544

用户确认目标为原生 Linux C++。
[libfaketime #544](https://github.com/wolfcw/libfaketime/issues/544) 描述的是 Wine/Proton 游戏挂起，
其中零偏移也能触发问题。截至本次读取，该 issue 没有确认根因，不能直接据此判断本项目的问题。
本次借用了其中“零偏移也应测试”和“反复切换日期”的复现方式。

探针让真正的主线程和四个工作线程持续调用 `clock_gettime`、`gettimeofday` 和 `time`，
分别测试纯读时、共享互斥锁争用、每毫秒 `SIGALRM` 三种模式，另有控制线程和 5 ms 定时线程。
每种模式执行 8 次新进程首次零偏移、同进程 60 次零偏移、27 次前后调和归零切换，
合计 30 个会话、285 次 CLI 注入。修复后全部通过，读数、各线程、定时器及信号处理均有进度，
所有检查到的线程均已解除 ptrace。

确定性回归还覆盖：主线程已有待处理 `SIGUSR1` 时远程调用正常完成且信号随后恰好投递一次；
初始信号暂停不被误当成中断暂停；`SIGSTOP` 中断远程 `mmap` 时恢复原状态、不执行后备重试，
解除跟踪后目标确实保持停止，测试发送 `SIGCONT` 后继续运行。远程调用成功与中断两条路径均比较
原始寄存器、指令字节和非空信号屏蔽设置。

上述测试复现的是确定的注入失败，以及单独列出的绝对等待问题；没有复现用户原程序的完整挂起现场。
信号处理依据 [Linux ptrace 手册](https://man7.org/linux/man-pages/man2/ptrace.2.html)：
单步期间也可能因普通信号暂停，不能仅凭 `waitpid` 返回就认定指令完成。

## 尚未修复的资源增长

同一目标进程重复执行 CLI 注入，每次增加约 12 KiB 匿名 RWX 映射。本机页大小为 4 KiB，
首轮每种运行方式经过 30 次注入后均增加 368,640 B（360 KiB）；运行时自身的 JIT 映射也可能
叠加到匿名 RWX 总量中。偏移数值正常替换，没有累加错，
但旧的注入映射没有回收或复用。长时间反复注入时应将其视为已知问题。

## 复现与证据

运行 `make test_compat`；也可按 [README](README.md) 选择语言。
构建产物统一位于 `test/bin/compat-build-*`，普通 C 测试和 C++ 回归产物也在 `test/bin`。

首轮完整原始数据位于 [results.json](../bin/compat-2026-11-13/results.json)，
追加信号修复后的完整矩阵位于 [results.json](../bin/compat-signals-final-2026-11-13/results.json)，
285 次原生 C++ CLI 压力测试位于 [results.json](../bin/cpp-stress-final/results.json)。
各目录还包含逐进程日志、构建日志和汇总数据；修复前的信号失败日志保存在 `test/bin/issue544`。
`make test`、`go test -race -count=1 . ./cmd`、`go vet . ./cmd` 均通过。
仓库既有的 `example` 目录混放 C/Go 源文件，`go test ./...` 会被该布局阻挡；
本轮 Go 包检查限定为 `. ./cmd`，跨语言验证通过 `make test_compat` 执行。
