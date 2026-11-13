#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <cstdlib>
#include <iostream>
#include <mutex>
#include <sys/syscall.h>
#include <sys/time.h>
#include <thread>
#include <unistd.h>
#include <vector>

using namespace std::chrono;

static int64_t real_nanoseconds()
{
    timespec ts;
    if (syscall(SYS_clock_gettime, CLOCK_REALTIME, &ts) != 0)
        std::exit(EXIT_FAILURE);
    return int64_t(ts.tv_sec) * 1000000000 + ts.tv_nsec;
}

int main(int argc, char **argv)
{
    if (argc != 2)
        return EXIT_FAILURE;
    const int64_t delta = std::strtoll(argv[1], nullptr, 10);

    // Let the parent inject while this process is blocked on input.
    std::cout << "READY" << std::endl;
    if (std::cin.get() == std::char_traits<char>::eof())
        return EXIT_FAILURE;

    std::atomic<bool> valid{true};
    std::vector<std::thread> workers;
    for (int i = 0; i < 4; ++i) {
        workers.emplace_back([&] {
            for (int j = 0; j < 1000; ++j) {
                auto before = real_nanoseconds();
                auto now = duration_cast<nanoseconds>(system_clock::now().time_since_epoch()).count();
                auto after = real_nanoseconds();
                if (now < before + delta || now > after + delta)
                    valid = false;

                timeval tv;
                before = real_nanoseconds() / 1000;
                if (gettimeofday(&tv, nullptr) != 0) {
                    valid = false;
                    return;
                }
                after = real_nanoseconds() / 1000;
                now = int64_t(tv.tv_sec) * 1000000 + tv.tv_usec;
                if (now < before + delta / 1000 || now > after + delta / 1000 ||
                    tv.tv_usec < 0 || tv.tv_usec >= 1000000)
                    valid = false;
            }
        });
    }
    for (auto &worker : workers)
        worker.join();
    if (!valid)
        return EXIT_FAILURE;

    // Relative waits must continue to make progress after changing wall time.
    auto start = steady_clock::now();
    std::this_thread::sleep_for(milliseconds(20));
    std::mutex mutex;
    std::condition_variable cv;
    std::unique_lock<std::mutex> lock(mutex);
    if (cv.wait_for(lock, milliseconds(20), [] { return false; }))
        return EXIT_FAILURE;
    auto elapsed = steady_clock::now() - start;
    if (elapsed < milliseconds(40) || elapsed > seconds(2))
        return EXIT_FAILURE;

    std::cout << "OK" << std::endl;
    return EXIT_SUCCESS;
}
