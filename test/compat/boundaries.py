"""Parent/child fixture for checking fork, exec and descendant discovery."""
import ctypes
import json
import os
import signal
import subprocess
import sys
import time


def emit(value):
    print(json.dumps(value), flush=True)


def child(name):
    libc = ctypes.CDLL(None)
    if libc.prctl(1, signal.SIGKILL, 0, 0, 0) != 0:
        raise RuntimeError("PR_SET_PDEATHSIG failed")
    if libc.prctl(15, name.encode(), 0, 0, 0) != 0:
        raise RuntimeError("PR_SET_NAME failed")
    print("READY", flush=True)
    for line in sys.stdin:
        if line.strip() == "sample":
            emit({"time": time.time(), "pid": os.getpid()})


def main():
    children = {}
    try:
        for name in ["clockworker", "clock worker"]:
            process = subprocess.Popen([sys.executable, "-u", __file__, "--child", name],
                                       stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
            if process.stdout.readline().strip() != "READY":
                raise RuntimeError("child startup failed")
            children[name] = process
        print("READY", flush=True)
        for line in sys.stdin:
            command = line.strip()
            if command == "sample":
                values = {"parent": {"time": time.time(), "pid": os.getpid()}}
                for name, process in children.items():
                    process.stdin.write("sample\n")
                    process.stdin.flush()
                    values[name] = json.loads(process.stdout.readline())
                emit(values)
            elif command == "fork":
                reader, writer = os.pipe()
                pid = os.fork()
                if pid == 0:
                    os.close(reader)
                    os.write(writer, json.dumps({"time": time.time(), "pid": os.getpid()}).encode())
                    os._exit(0)
                os.close(writer)
                with os.fdopen(reader) as stream:
                    value = json.load(stream)
                os.waitpid(pid, 0)
                emit(value)
            elif command == "exec":
                output = subprocess.check_output([sys.executable, "-c", "import time; print(time.time())"],
                                                 text=True, timeout=3)
                emit({"time": float(output)})
            elif command == "bye":
                return
    finally:
        for process in children.values():
            process.stdin.close()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--child":
        child(sys.argv[2])
    else:
        main()
