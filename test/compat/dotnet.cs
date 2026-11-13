using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

class ClockProbe
{
    static long counter;
    static double Wall(DateTime value) => (value.ToUniversalTime() - DateTime.UnixEpoch).TotalSeconds;
    static void Main()
    {
        for (int i = 0; i < 4; i++)
            new Thread(() => { while (true) { for (int j = 0; j < 100; j++) _ = DateTime.UtcNow; Interlocked.Add(ref counter, 100); Thread.Sleep(1); } }) { IsBackground = true }.Start();
        Console.WriteLine("READY");
        for (string? line; (line = Console.ReadLine()) != null;)
        {
            if (line == "sample")
                Console.WriteLine(JsonSerializer.Serialize(new {
                    wall = new Dictionary<string, double> {
                        ["DateTime.UtcNow"] = Wall(DateTime.UtcNow),
                        ["DateTime.Now"] = Wall(DateTime.Now),
                        ["DateTimeOffset.UtcNow"] = (DateTimeOffset.UtcNow - DateTimeOffset.UnixEpoch).TotalSeconds },
                    mono = (double)Stopwatch.GetTimestamp() / Stopwatch.Frequency,
                    counter = Interlocked.Read(ref counter) }));
            else if (line == "timers")
            {
                var relative = new Dictionary<string, double>();
                var timer = Stopwatch.StartNew();
                Thread.Sleep(20);
                relative["Thread.Sleep"] = timer.Elapsed.TotalSeconds;
                timer.Restart();
                Task.Delay(20).GetAwaiter().GetResult();
                relative["Task.Delay"] = timer.Elapsed.TotalSeconds;
                timer.Restart();
                using (var sem = new SemaphoreSlim(0)) sem.Wait(20);
                relative["SemaphoreSlim.Wait"] = timer.Elapsed.TotalSeconds;
                timer.Restart();
                object monitor = new();
                lock (monitor) Monitor.Wait(monitor, 20);
                relative["Monitor.Wait"] = timer.Elapsed.TotalSeconds;
                Console.WriteLine(JsonSerializer.Serialize(new { relative }));
            }
            else if (line == "bye") return;
        }
    }
}
