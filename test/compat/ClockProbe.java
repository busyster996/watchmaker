import java.io.*;
import java.time.*;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import java.util.concurrent.locks.*;

public class ClockProbe {
    static final AtomicLong counter = new AtomicLong();
    static double seconds(long nanos) { return nanos / 1e9; }
    static double instant(Instant value) { return value.getEpochSecond() + value.getNano() / 1e9; }
    static void relative(String name, Runnable action) {
        long start = System.nanoTime();
        action.run();
        System.out.printf(Locale.ROOT, "\"%s\":%.9f,", name, seconds(System.nanoTime() - start));
    }
    public static void main(String[] args) throws Exception {
        for (int i = 0; i < 4; i++) {
            Thread worker = new Thread(() -> {
                try {
                    while (true) {
                        for (int j = 0; j < 100; j++) Instant.now();
                        counter.addAndGet(100);
                        Thread.sleep(1);
                    }
                } catch (InterruptedException ex) { Thread.currentThread().interrupt(); }
            });
            worker.setDaemon(true);
            worker.start();
        }
        // Resolve the date APIs before reporting readiness.
        OffsetDateTime.now(ZoneOffset.UTC);
        System.out.println("READY");
        BufferedReader input = new BufferedReader(new InputStreamReader(System.in));
        for (String line; (line = input.readLine()) != null;) {
            if (line.equals("sample")) {
                System.out.printf(Locale.ROOT,
                    "{\"wall\":{\"currentTimeMillis\":%.9f,\"Instant.now\":%.9f,\"Date\":%.9f,\"OffsetDateTime\":%.9f},\"mono\":%.9f,\"counter\":%d}%n",
                    System.currentTimeMillis() / 1e3, instant(Instant.now()), new Date().getTime() / 1e3,
                    instant(OffsetDateTime.now(ZoneOffset.UTC).toInstant()), seconds(System.nanoTime()), counter.get());
            } else if (line.equals("timers")) {
                System.out.print("{\"relative\":{");
                relative("Thread.sleep", () -> { try { Thread.sleep(20); } catch (InterruptedException e) { throw new RuntimeException(e); } });
                relative("Object.wait", () -> { Object monitor = new Object(); synchronized (monitor) { try { monitor.wait(20); } catch (InterruptedException e) { throw new RuntimeException(e); } } });
                relative("CountDownLatch", () -> { try { new CountDownLatch(1).await(20, TimeUnit.MILLISECONDS); } catch (InterruptedException e) { throw new RuntimeException(e); } });
                relative("parkNanos", () -> LockSupport.parkNanos(20_000_000));
                relative("ScheduledExecutor", () -> {
                    ScheduledExecutorService service = Executors.newSingleThreadScheduledExecutor();
                    try { service.schedule(() -> {}, 20, TimeUnit.MILLISECONDS).get(1, TimeUnit.SECONDS); }
                    catch (Exception e) { throw new RuntimeException(e); }
                    finally { service.shutdownNow(); }
                });
                System.out.print("\"dummy\":0},\"waits\":{");
                AtomicLong elapsed = new AtomicLong();
                Thread parked = new Thread(() -> {
                    long start = System.nanoTime();
                    LockSupport.parkUntil(System.currentTimeMillis() + 50);
                    elapsed.set(System.nanoTime() - start);
                });
                parked.start();
                Thread.sleep(100);
                LockSupport.unpark(parked);
                parked.join(1000);
                if (parked.isAlive()) throw new RuntimeException("parkUntil did not respond to rescue");
                System.out.printf(Locale.ROOT, "\"parkUntil\":{\"elapsed\":%.9f}}}%n", seconds(elapsed.get()));
            } else if (line.equals("bye")) return;
        }
    }
}
