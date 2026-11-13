import asyncio
import datetime
import json
import sys
import threading
import time

counter = 0


def worker():
    global counter
    while True:
        for _ in range(100):
            time.time_ns()
        counter += 100
        time.sleep(0.001)


for _ in range(4):
    threading.Thread(target=worker, daemon=True).start()
print("READY", flush=True)

for command in sys.stdin:
    if command.strip() == "sample":
        print(json.dumps({
            "wall": {
                "time.time": time.time(),
                "time.time_ns": time.time_ns() / 1e9,
                "datetime.now": datetime.datetime.now(datetime.timezone.utc).timestamp(),
                "clock_gettime": time.clock_gettime(time.CLOCK_REALTIME),
            },
            "unskewed_wall": {"realtime_coarse": time.clock_gettime(5)},
            "mono": time.monotonic(), "cpu": time.process_time(), "counter": counter,
        }), flush=True)
    elif command.strip() == "timers":
        relative = {}
        start = time.monotonic()
        time.sleep(0.02)
        relative["time.sleep"] = time.monotonic() - start
        start = time.monotonic()
        threading.Event().wait(0.02)
        relative["Event.wait"] = time.monotonic() - start
        condition = threading.Condition()
        start = time.monotonic()
        with condition:
            condition.wait(0.02)
        relative["Condition.wait"] = time.monotonic() - start
        done = threading.Event()
        start = time.monotonic()
        threading.Timer(0.02, done.set).start()
        if not done.wait(1):
            raise RuntimeError("threading.Timer stalled")
        relative["threading.Timer"] = time.monotonic() - start
        start = time.monotonic()
        asyncio.run(asyncio.sleep(0.02))
        relative["asyncio.sleep"] = time.monotonic() - start
        print(json.dumps({"relative": relative}), flush=True)
    elif command.strip() == "bye":
        break
