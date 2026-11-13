//go:build linux && (amd64 || arm64)

package watchmaker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type cppThreadSample struct {
	Wall    float64
	Counts  [5]uint64
	Ticks   uint64
	Signals uint64
	Users   uint64
}

type cppThreadProbe struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	stderr *bytes.Buffer
}

func buildCPPThreadProbe(t *testing.T) string {
	t.Helper()
	compiler, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is required for the C++ thread/signal tests")
	}
	if err := os.MkdirAll("test/bin", 0755); err != nil {
		t.Fatal(err)
	}
	buildDir, err := os.MkdirTemp("test/bin", "cpp-signals-")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := filepath.Abs(filepath.Join(buildDir, "cpp_signals"))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command(compiler, "-std=c++17", "-O2", "-pthread", "-Wall", "-Wextra", "-o", executable, "test/test_cpp_signals.cpp")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build C++ target: %v\n%s", err, output)
	}
	return executable
}

func startCPPThreadProbe(t *testing.T, executable, mode string) *cppThreadProbe {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	p := &cppThreadProbe{cmd: exec.CommandContext(ctx, executable, mode), stderr: new(bytes.Buffer)}
	p.cmd.Stderr = p.stderr
	var err error
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.stdout = bufio.NewScanner(stdout)
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		_ = p.stdin.Close()
	})
	if !p.stdout.Scan() || p.stdout.Text() != "READY" {
		t.Fatalf("C++ target not ready: %v\n%s", p.stdout.Err(), p.stderr)
	}
	return p
}

func (p *cppThreadProbe) sample(t *testing.T, offset time.Duration) cppThreadSample {
	t.Helper()
	before := time.Now().Add(offset)
	if _, err := io.WriteString(p.stdin, "sample\n"); err != nil {
		t.Fatal(err)
	}
	if !p.stdout.Scan() {
		t.Fatalf("C++ target stopped responding: %v\n%s", p.stdout.Err(), p.stderr)
	}
	after := time.Now().Add(offset)
	var sample cppThreadSample
	if err := json.Unmarshal(p.stdout.Bytes(), &sample); err != nil {
		t.Fatal(err)
	}
	lo, hi := float64(before.UnixNano())/1e9-.05, float64(after.UnixNano())/1e9+.05
	if sample.Wall < lo || sample.Wall > hi {
		t.Fatalf("wall time %f outside [%f, %f]", sample.Wall, lo, hi)
	}
	return sample
}

func (p *cppThreadProbe) progress(t *testing.T, offset time.Duration, signals bool) {
	t.Helper()
	first := p.sample(t, offset)
	deadline := time.Now().Add(time.Second)
	for {
		time.Sleep(25 * time.Millisecond)
		last := p.sample(t, offset)
		ok := last.Ticks > first.Ticks && (!signals || last.Signals > first.Signals)
		for i, count := range first.Counts {
			ok = ok && last.Counts[i] > count
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("C++ thread, timer, or signal stopped progressing: before=%+v after=%+v", first, last)
		}
	}
}

func TestCPPThreadProgress(t *testing.T) {
	executable := buildCPPThreadProbe(t)
	for _, mode := range []string{"clock", "mutex", "signals"} {
		t.Run(mode, func(t *testing.T) {
			p := startCPPThreadProbe(t, executable, mode)
			p.progress(t, 0, mode == "signals")
			const day = 24 * time.Hour
			for _, offset := range []time.Duration{0, -415 * day, 0, day, 0, -400 * day, 0, 120 * time.Second, -120 * time.Second, 0} {
				// Fresh images exercise remote mmap on every injection, like the CLI.
				skew, err := GetSkew(NewConfig(0, offset.Nanoseconds(), 1))
				if err != nil {
					t.Fatal(err)
				}
				if err := skew.Inject(uint64(p.cmd.Process.Pid)); err != nil {
					t.Fatalf("inject %v: %v", offset, err)
				}
				p.progress(t, offset, mode == "signals")
				assertThreadsDetached(t, p.cmd.Process.Pid)
			}
		})
	}
}

func assertThreadsDetached(t *testing.T, pid int) {
	t.Helper()
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/status", pid, task.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "TracerPid:") && strings.TrimSpace(strings.TrimPrefix(line, "TracerPid:")) != "0" {
				t.Fatalf("thread %s still traced: %s", task.Name(), line)
			}
		}
	}
}

func waitThreadState(t *testing.T, pid int, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(status, []byte("State:\t"+state)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("thread %d did not reach state %s", pid, state)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPtraceSignals(t *testing.T) {
	executable := buildCPPThreadProbe(t)
	for _, signal := range []unix.Signal{unix.SIGUSR1, unix.SIGSTOP} {
		t.Run("pending_during_syscall_"+signal.String(), func(t *testing.T) {
			p := startCPPThreadProbe(t, executable, "signals")
			pid := p.cmd.Process.Pid
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			program, err := Trace(pid)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if program != nil {
					_ = program.Detach()
				}
			}()
			var beforeRegs, afterRegs unix.PtraceRegs
			if err := getRegs(pid, &beforeRegs); err != nil {
				t.Fatal(err)
			}
			beforeCode, afterCode := make([]byte, unixInstrSize), make([]byte, unixInstrSize)
			if _, err := unix.PtracePeekData(pid, getIp(&beforeRegs), beforeCode); err != nil {
				t.Fatal(err)
			}
			var beforeMask, afterMask uint64
			if err := ptraceSignalMask(unix.PTRACE_GETSIGMASK, pid, &beforeMask); err != nil {
				t.Fatal(err)
			}
			if beforeMask&(uint64(1)<<(unix.SIGUSR2-1)) == 0 {
				t.Fatal("target's initial SIGUSR2 mask is missing")
			}
			// A thread-directed signal pending before SINGLESTEP deterministically
			// exposed the old bug: RAX still held SYS_GETPID, not the syscall result.
			if err := unix.Tgkill(pid, pid, signal); err != nil {
				t.Fatal(err)
			}
			var result uint64
			if signal == unix.SIGSTOP {
				// Exercise mmap's failure path too: retrying a failed single step
				// would suppress the signal-delivery stop before detach.
				result, err = program.Mmap(4096, 0)
			} else {
				result, err = program.Syscall(unix.SYS_GETPID)
			}
			if signal == unix.SIGSTOP {
				if err == nil {
					t.Fatal("uncatchable SIGSTOP should interrupt the remote syscall")
				}
			} else if err != nil || result != uint64(pid) {
				t.Fatalf("remote getpid with pending signal: result=%d want=%d err=%v", result, pid, err)
			}
			if err := getRegs(pid, &afterRegs); err != nil {
				t.Fatal(err)
			}
			if _, err := unix.PtracePeekData(pid, getIp(&beforeRegs), afterCode); err != nil {
				t.Fatal(err)
			}
			if err := ptraceSignalMask(unix.PTRACE_GETSIGMASK, pid, &afterMask); err != nil {
				t.Fatal(err)
			}
			if beforeRegs != afterRegs || !bytes.Equal(beforeCode, afterCode) || beforeMask != afterMask {
				t.Fatalf("syscall failed to restore target state: regs=%v code=%v mask=%#x/%#x", beforeRegs == afterRegs, bytes.Equal(beforeCode, afterCode), beforeMask, afterMask)
			}
			if err := program.Detach(); err != nil {
				t.Fatal(err)
			}
			program = nil
			if signal == unix.SIGSTOP {
				// The failed syscall must preserve the stop signal at detach. Resume
				// the test target only after observing that it really stopped.
				waitThreadState(t, pid, "T")
				if err := unix.Kill(pid, unix.SIGCONT); err != nil {
					t.Fatal(err)
				}
			}
			p.progress(t, 0, true)
			if sample := p.sample(t, 0); signal == unix.SIGUSR1 && sample.Users != 1 {
				t.Fatalf("pending SIGUSR1 not delivered exactly once: %+v", sample)
			}
			assertThreadsDetached(t, pid)
		})
	}
	t.Run("signal_before_interrupt", func(t *testing.T) {
		p := startCPPThreadProbe(t, executable, "clock")
		pid := p.cmd.Process.Pid
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := unix.PtraceSeize(pid); err != nil {
			t.Fatal(err)
		}
		signal := 0
		detached := false
		defer func() {
			if !detached {
				_ = detachThread(pid, signal)
			}
		}()
		if err := unix.Tgkill(pid, pid, unix.SIGUSR1); err != nil {
			t.Fatal(err)
		}
		// Leave the signal-delivery stop waiting for interruptThread's waitpid.
		waitThreadState(t, pid, "t")
		var err error
		signal, err = interruptThread(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := detachThread(pid, 0); err != nil {
			t.Fatal(err)
		}
		detached = true
		p.progress(t, 0, false)
		if sample := p.sample(t, 0); sample.Users != 1 {
			t.Fatalf("signal racing with interrupt was lost: %+v", sample)
		}
		assertThreadsDetached(t, pid)
	})
}
