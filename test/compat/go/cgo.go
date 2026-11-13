package main

/*
#include <time.h>
#include <sys/time.h>
static double c_wall(void) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (double)ts.tv_sec + ts.tv_nsec / 1e9;
}
static double c_timeofday(void) {
    struct timeval tv;
    gettimeofday(&tv, 0);
    return (double)tv.tv_sec + tv.tv_usec / 1e6;
}
*/
import "C"

func init() {
	extraWall["cgo.clock_gettime"] = func() float64 { return float64(C.c_wall()) }
	extraWall["cgo.gettimeofday"] = func() float64 { return float64(C.c_timeofday()) }
}
