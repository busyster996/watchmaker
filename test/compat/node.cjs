const { Worker, isMainThread, workerData } = require('node:worker_threads');
const { performance } = require('node:perf_hooks');
const readline = require('node:readline');

if (!isMainThread) {
  const count = new Int32Array(workerData);
  for (;;) {
    for (let i = 0; i < 100; i++) Date.now();
    Atomics.add(count, 0, 100);
    Atomics.wait(count, 1, 0, 1);
  }
} else {
  const buffer = new SharedArrayBuffer(8);
  const count = new Int32Array(buffer);
  for (let i = 0; i < 4; i++) new Worker(__filename, { workerData: buffer });
  console.log('READY');
  const lines = readline.createInterface({ input: process.stdin });
  (async () => {
    for await (const line of lines) {
      if (line === 'sample') {
        console.log(JSON.stringify({
          wall: { 'Date.now': Date.now() / 1e3, 'new Date': new Date().getTime() / 1e3 },
          unskewed_wall: { 'performance.timeOrigin+now': (performance.timeOrigin + performance.now()) / 1e3 },
          mono: Number(process.hrtime.bigint()) / 1e9, counter: Atomics.load(count, 0),
        }));
      } else if (line === 'timers') {
        const start = performance.now();
        await new Promise(resolve => setTimeout(resolve, 20));
        console.log(JSON.stringify({ relative: { setTimeout: (performance.now() - start) / 1e3 } }));
      } else if (line === 'bye') process.exit(0);
    }
    process.exit(0);
  })().catch(error => { console.error(error); process.exit(1); });
}
