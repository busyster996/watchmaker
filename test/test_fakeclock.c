#include <errno.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/syscall.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

int64_t TV_SEC_DELTA;
int64_t TV_NSEC_DELTA;
uint64_t CLOCK_IDS_MASK = UINT64_C(1) << CLOCK_REALTIME;

int fake_clock_gettime(clockid_t clk_id, struct timespec *tp);
int fake_gettimeofday(struct timeval *tv, struct timezone *tz);

#define CHECK(condition) do { \
    if (!(condition)) { \
        fprintf(stderr, "%s:%d: %s\n", __FILE__, __LINE__, #condition); \
        exit(EXIT_FAILURE); \
    } \
} while (0)

static __int128 nanoseconds(struct timespec ts)
{
    return (__int128)ts.tv_sec * 1000000000 + ts.tv_nsec;
}

static __int128 microseconds(struct timeval tv)
{
    return (__int128)tv.tv_sec * 1000000 + tv.tv_usec;
}

static void check_offset(void)
{
    struct timespec before_ts, actual_ts, after_ts;
    CHECK(syscall(SYS_clock_gettime, CLOCK_REALTIME, &before_ts) == 0);
    CHECK(fake_clock_gettime(CLOCK_REALTIME, &actual_ts) == 0);
    CHECK(syscall(SYS_clock_gettime, CLOCK_REALTIME, &after_ts) == 0);

    __int128 delta_ns = (__int128)TV_SEC_DELTA * 1000000000 + TV_NSEC_DELTA;
    CHECK(actual_ts.tv_nsec >= 0 && actual_ts.tv_nsec < 1000000000);
    CHECK(nanoseconds(actual_ts) >= nanoseconds(before_ts) + delta_ns);
    CHECK(nanoseconds(actual_ts) <= nanoseconds(after_ts) + delta_ns);

    struct timeval before_tv, actual_tv, after_tv;
    CHECK(syscall(SYS_gettimeofday, &before_tv, NULL) == 0);
    CHECK(fake_gettimeofday(&actual_tv, NULL) == 0);
    CHECK(syscall(SYS_gettimeofday, &after_tv, NULL) == 0);

    __int128 nsec = TV_NSEC_DELTA;
    __int128 delta_us = (__int128)TV_SEC_DELTA * 1000000;
    delta_us += nsec >= 0 ? (nsec + 500) / 1000 : -((-nsec + 500) / 1000);
    CHECK(actual_tv.tv_usec >= 0 && actual_tv.tv_usec < 1000000);
    CHECK(microseconds(actual_tv) >= microseconds(before_tv) + delta_us);
    CHECK(microseconds(actual_tv) <= microseconds(after_tv) + delta_us);
}

int main(void)
{
    const int64_t offsets[][2] = {
        {0, 0},
        {0, 1000000000},
        {0, -1000000000},
        {2, 1500000000},
        {-2, -1500000000},
        {0, 500},
        {0, -500},
        {0, 999999999},
        {0, -999999999},
        {17, INT64_MAX},
        {-17, INT64_MIN},
        {0, 725760000000000000},
        {0, -725760000000000000},
    };
    for (size_t i = 0; i < sizeof(offsets) / sizeof(offsets[0]); ++i) {
        TV_SEC_DELTA = offsets[i][0];
        TV_NSEC_DELTA = offsets[i][1];
        check_offset();
    }

    // Large offsets must remain cheap even when a program reads time frequently.
    TV_SEC_DELTA = 0;
    TV_NSEC_DELTA = -725760000000000000;
    struct timespec start, end;
    CHECK(syscall(SYS_clock_gettime, CLOCK_MONOTONIC, &start) == 0);
    for (int i = 0; i < 1000; ++i) {
        check_offset();
    }
    CHECK(syscall(SYS_clock_gettime, CLOCK_MONOTONIC, &end) == 0);
    CHECK(nanoseconds(end) - nanoseconds(start) < 1000000000);

    // Clocks outside the mask, including dynamic clock IDs, keep their real time.
    struct timespec before, actual, after;
    CHECK(syscall(SYS_clock_gettime, CLOCK_MONOTONIC, &before) == 0);
    CHECK(fake_clock_gettime(CLOCK_MONOTONIC, &actual) == 0);
    CHECK(syscall(SYS_clock_gettime, CLOCK_MONOTONIC, &after) == 0);
    CHECK(nanoseconds(actual) >= nanoseconds(before));
    CHECK(nanoseconds(actual) <= nanoseconds(after));

    CLOCK_IDS_MASK = UINT64_MAX;
    clockid_t cpu_clock;
    CHECK(clock_getcpuclockid(getpid(), &cpu_clock) == 0);
    CHECK(cpu_clock < 0);
    CHECK(syscall(SYS_clock_gettime, cpu_clock, &before) == 0);
    CHECK(fake_clock_gettime(cpu_clock, &actual) == 0);
    CHECK(syscall(SYS_clock_gettime, cpu_clock, &after) == 0);
    CHECK(nanoseconds(actual) >= nanoseconds(before));
    CHECK(nanoseconds(actual) <= nanoseconds(after));

    actual = (struct timespec){123, 456};
    CHECK(fake_clock_gettime(-1, &actual) == -EINVAL);
    CHECK(actual.tv_sec == 123 && actual.tv_nsec == 456);
    CHECK(fake_clock_gettime(CLOCK_REALTIME, NULL) == -EFAULT);
    CHECK(fake_gettimeofday((struct timeval *)1, NULL) == -EFAULT);
    CHECK(fake_gettimeofday(NULL, NULL) == 0);

    puts("fakeclock: large offsets, clock selection and syscall errors passed");
    return EXIT_SUCCESS;
}
