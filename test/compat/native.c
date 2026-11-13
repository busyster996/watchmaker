#define _GNU_SOURCE 1
#include <errno.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/syscall.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

#ifdef __cplusplus
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <mutex>
#include <thread>
static std::atomic<unsigned long> progress(0);
#define ADD_PROGRESS(n) progress.fetch_add(n)
#define GET_PROGRESS() progress.load()
#else
#include <stdatomic.h>
static atomic_ulong progress;
#define ADD_PROGRESS(n) atomic_fetch_add(&progress, n)
#define GET_PROGRESS() atomic_load(&progress)
#endif

static double stamp(clockid_t clock)
{
    struct timespec ts;
    if (clock_gettime(clock, &ts)) _exit(2);
    return (double)ts.tv_sec + ts.tv_nsec / 1e9;
}

static double raw_wall(void)
{
    struct timespec ts;
    if (syscall(SYS_clock_gettime, CLOCK_REALTIME, &ts)) _exit(2);
    return (double)ts.tv_sec + ts.tv_nsec / 1e9;
}

static void *worker(void *unused)
{
    (void)unused;
    for (;;) {
        for (int i = 0; i < 100; ++i) {
            struct timeval tv;
            (void)stamp(CLOCK_REALTIME);
            (void)time(NULL);
            if (gettimeofday(&tv, NULL)) _exit(2);
        }
        ADD_PROGRESS(100);
        usleep(1000);
    }
    return NULL;
}

struct wait_case {
    pthread_mutex_t mutex;
    pthread_cond_t cond;
    clockid_t clock;
    int rescue;
    int result;
    double elapsed;
};

static void *waiter(void *arg)
{
    struct wait_case *test = (struct wait_case *)arg;
    pthread_mutex_lock(&test->mutex);
    struct timespec until;
    clock_gettime(test->clock, &until);
    until.tv_nsec += 50000000;
    if (until.tv_nsec >= 1000000000) {
        until.tv_sec++;
        until.tv_nsec -= 1000000000;
    }
    double start = stamp(CLOCK_MONOTONIC);
    test->result = test->rescue ? EINTR : pthread_cond_timedwait(&test->cond, &test->mutex, &until);
    test->elapsed = stamp(CLOCK_MONOTONIC) - start;
    pthread_mutex_unlock(&test->mutex);
    return NULL;
}

static void cond_case(const char *name, clockid_t clock)
{
    struct wait_case test;
    memset(&test, 0, sizeof(test));
    test.clock = clock;
    pthread_condattr_t attr;
    pthread_condattr_init(&attr);
    pthread_condattr_setclock(&attr, clock);
    pthread_mutex_init(&test.mutex, NULL);
    pthread_cond_init(&test.cond, &attr);
    pthread_condattr_destroy(&attr);
    pthread_t thread;
    if (pthread_create(&thread, NULL, waiter, &test)) _exit(3);
    usleep(100000);
    pthread_mutex_lock(&test.mutex);
    test.rescue = 1;
    pthread_cond_broadcast(&test.cond);
    pthread_mutex_unlock(&test.mutex);
    pthread_join(thread, NULL);
    printf(",\"%s\":{\"elapsed\":%.9f,\"timeout\":%s}", name, test.elapsed,
           test.result == ETIMEDOUT ? "true" : "false");
    pthread_cond_destroy(&test.cond);
    pthread_mutex_destroy(&test.mutex);
}

#ifdef __cplusplus
static void cpp_cond_case(bool steady)
{
    using namespace std::chrono;
    std::mutex mutex;
    std::condition_variable cv;
    bool rescue = false, timed_out = false;
    double elapsed = 0;
    std::thread thread([&] {
        std::unique_lock<std::mutex> lock(mutex);
        auto start = steady_clock::now();
        if (!rescue) {
            auto status = steady ? cv.wait_until(lock, steady_clock::now() + milliseconds(50))
                                 : cv.wait_until(lock, system_clock::now() + milliseconds(50));
            timed_out = status == std::cv_status::timeout;
        }
        elapsed = duration<double>(steady_clock::now() - start).count();
    });
    std::this_thread::sleep_for(milliseconds(100));
    {
        std::lock_guard<std::mutex> lock(mutex);
        rescue = true;
        cv.notify_all();
    }
    thread.join();
    printf(",\"%s\":{\"elapsed\":%.9f,\"timeout\":%s}",
           steady ? "cpp_cond_steady" : "cpp_cond_system", elapsed, timed_out ? "true" : "false");
}
#endif

int main(void)
{
    setbuf(stdout, NULL);
    for (int i = 0; i < 4; ++i) {
        pthread_t thread;
        if (pthread_create(&thread, NULL, worker, NULL)) return 3;
        pthread_detach(thread);
    }
    puts("READY");
    char command[64];
    while (fgets(command, sizeof(command), stdin)) {
        if (!strncmp(command, "sample", 6)) {
            struct timeval tv;
            gettimeofday(&tv, NULL);
            printf("{\"wall\":{\"time\":%.0f,\"gettimeofday\":%.9f,\"clock_gettime\":%.9f",
                   (double)time(NULL), tv.tv_sec + tv.tv_usec / 1e6, stamp(CLOCK_REALTIME));
#ifndef __cplusplus
            struct timespec ts;
            timespec_get(&ts, TIME_UTC);
            printf(",\"timespec_get\":%.9f", ts.tv_sec + ts.tv_nsec / 1e9);
#else
            using namespace std::chrono;
            printf(",\"system_clock\":%.9f,\"high_resolution_clock\":%.9f",
                   duration<double>(system_clock::now().time_since_epoch()).count(),
                   duration<double>(high_resolution_clock::now().time_since_epoch()).count());
#endif
            printf("},\"unskewed_wall\":{\"syscall_clock_gettime\":%.9f,\"realtime_coarse\":%.9f},"
                   "\"mono\":%.9f,\"cpu\":%.9f,\"counter\":%lu}\n",
                   raw_wall(), stamp(CLOCK_REALTIME_COARSE), stamp(CLOCK_MONOTONIC),
                   stamp(CLOCK_PROCESS_CPUTIME_ID), GET_PROGRESS());
        } else if (!strncmp(command, "timers", 6)) {
            double start = stamp(CLOCK_MONOTONIC);
            usleep(20000);
            printf("{\"relative\":{\"usleep\":%.9f", stamp(CLOCK_MONOTONIC) - start);
#ifdef __cplusplus
            using namespace std::chrono;
            auto begin = steady_clock::now();
            std::this_thread::sleep_for(milliseconds(20));
            printf(",\"cpp_sleep_for\":%.9f", duration<double>(steady_clock::now() - begin).count());
            begin = steady_clock::now();
            std::this_thread::sleep_until(system_clock::now() + milliseconds(20));
            printf(",\"cpp_sleep_until_system\":%.9f", duration<double>(steady_clock::now() - begin).count());
#endif
            printf("},\"waits\":{\"dummy\":null");
            cond_case("pthread_cond_monotonic", CLOCK_MONOTONIC);
            cond_case("pthread_cond_realtime", CLOCK_REALTIME);
#ifdef __cplusplus
            cpp_cond_case(true);
            cpp_cond_case(false);
#endif
            puts("}}");
        } else if (!strncmp(command, "bye", 3)) {
            return 0;
        }
    }
    return 0;
}
