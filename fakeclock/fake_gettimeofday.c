#include <time.h>
#include <sys/time.h>
#include <inttypes.h>
#include <syscall.h>

extern int64_t TV_SEC_DELTA;
extern int64_t TV_NSEC_DELTA;

#if defined(__amd64__)
inline int real_gettimeofday(struct timeval *tv, struct timezone *tz)
{
    int ret;
    asm volatile(
        "syscall"
        : "=a"(ret)
        : "0"(__NR_gettimeofday), "D"(tv), "S"(tz)
        : "rcx", "r11", "memory");

    return ret;
}

#elif defined(__aarch64__)
inline int real_gettimeofday(struct timeval *tv, struct timezone *tz)
{
    register int w0 __asm__("w0");

    register struct timeval *x0 __asm__("x0") = tv;
    register struct timezone *x1 __asm__("x1") = tz;
    register uint64_t w8 __asm__("w8") = __NR_gettimeofday; /* syscall number */
    __asm__ __volatile__(
        "svc 0;"
        : "+r"(w0)
        : "r"(x0), "r" (x1), "r"(w8)
        : "memory");

    return w0;
}
#endif

/* replace the libm call with integer rounding — no libm needed */
static inline int64_t div_round_nearest(int64_t n, int64_t d)
{
    return (n >= 0) ? (n + d/2) / d : -(( -n + d/2) / d);
}

int fake_gettimeofday(struct timeval *tv, struct timezone *tz)
{
    int ret = real_gettimeofday(tv, tz);
    if (ret != 0 || tv == NULL) {
        return ret;
    }

    const int64_t billion = 1000000000;
    const int64_t million = 1000000;
    const int64_t nsec_delta = TV_NSEC_DELTA;
    int64_t sec_delta = TV_SEC_DELTA + nsec_delta / billion;
    int64_t usec = tv->tv_usec + div_round_nearest(nsec_delta % billion, 1000);

    // Normalize after rounding so a carry never leaves tv_usec at 1000000.
    if (usec >= million)
    {
        sec_delta += 1;
        usec -= million;
    }
    else if (usec < 0)
    {
        sec_delta -= 1;
        usec += million;
    }

    tv->tv_sec += sec_delta;
    tv->tv_usec = usec;

    return ret;
}
