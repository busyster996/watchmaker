//go:build linux && (amd64 || arm64)

package watchmaker

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSkewCPP(t *testing.T) {
	compiler, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ is required for the C++ injection test")
	}

	source, err := os.ReadFile("test/test_cpp_time.cpp")
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join("test", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	buildDir, err := os.MkdirTemp(binDir, "cpp-")
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(buildDir, "test_cpp_time.cpp")
	if err := os.WriteFile(sourcePath, source, 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(buildDir, "test_cpp_time")
	build := exec.Command(compiler, "-std=c++17", "-O2", "-pthread", "-o", executable, sourcePath)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build C++ target: %v\n%s", err, output)
	}

	for _, offset := range []time.Duration{-23 * 365 * 24 * time.Hour, 23 * 365 * 24 * time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, strconv.FormatInt(offset.Nanoseconds(), 10))
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()

			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "READY" {
				t.Fatalf("C++ target did not become ready: %v\n%s", scanner.Err(), &stderr)
			}
			skew, err := GetSkew(NewConfig(0, offset.Nanoseconds(), 1))
			if err != nil {
				t.Fatal(err)
			}
			if err := skew.Inject(uint64(cmd.Process.Pid)); err != nil {
				t.Fatal(err)
			}
			if _, err := stdin.Write([]byte("\n")); err != nil {
				t.Fatal(err)
			}
			if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			if !scanner.Scan() || scanner.Text() != "OK" {
				t.Fatalf("C++ time reads or waits failed: context=%v, read=%v\n%s", ctx.Err(), scanner.Err(), &stderr)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("C++ target: %v\n%s", err, &stderr)
			}
		})
	}
}
