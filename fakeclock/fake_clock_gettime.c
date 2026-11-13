#include <time.h>
#include <inttypes.h>
#include <syscall.h>

extern int64_t TV_SEC_DELTA;
extern int64_t TV_NSEC_DELTA;
extern uint64_t CLOCK_IDS_MASK;

#if defined(__amd64__)
inline int real_clock_gettime(clockid_t clk_id, struct timespec *tp) {
    int ret;
    asm volatile
        (
            "syscall"
            : "=a" (ret)
            : "0"(__NR_clock_gettime), "D"(clk_id), "S"(tp)
            : "rcx", "r11", "memory"
        );

    return ret;
}
#elif defined(__aarch64__)
inline int real_clock_gettime(clockid_t clk_id, struct timespec *tp) {
    register clockid_t x0 __asm__ ("x0") = clk_id;
    register struct timespec *x1 __asm__ ("x1") = tp;
    register uint64_t w8 __asm__ ("w8") = __NR_clock_gettime; /* syscall number */
    __asm__ __volatile__ (
        "svc 0;"
        : "+r" (x0)
        : "r" (x0), "r" (x1), "r" (w8)
        : "memory"
    );

    return x0;
}
#endif

int fake_clock_gettime(clockid_t clk_id, struct timespec *tp) {
    int ret = real_clock_gettime(clk_id, tp);
    if (ret != 0) {
        return ret;
    }

    if (clk_id < 0 || clk_id >= 64 ||
        ((UINT64_C(1) << clk_id) & CLOCK_IDS_MASK) == 0) {
        return ret;
    }

    const int64_t billion = 1000000000;
    const int64_t nsec_delta = TV_NSEC_DELTA;
    // Split first so large date changes take constant time and cannot overflow
    // when adding the fractional part to the current nanoseconds.
    int64_t sec_delta = TV_SEC_DELTA + nsec_delta / billion;
    int64_t nsec = tp->tv_nsec + nsec_delta % billion;
    if (nsec >= billion) {
        sec_delta += 1;
        nsec -= billion;
    } else if (nsec < 0) {
        sec_delta -= 1;
        nsec += billion;
    }

    tp->tv_sec += sec_delta;
    tp->tv_nsec = nsec;

    return ret;
}
