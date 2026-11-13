#include <atomic>
#include <chrono>
#include <csignal>
#include <cstdio>
#include <iostream>
#include <mutex>
#include <string>
#include <sys/resource.h>
#include <sys/time.h>
#include <thread>
#include <time.h>
#include <unistd.h>
#include <vector>

std::atomic<bool> stopping{false};
std::atomic<unsigned long long> counters[5]{};
std::atomic<unsigned long long> clock_values[5]{};
std::atomic<unsigned long long> ticks{0};
std::atomic<unsigned int> signals_seen{0};
std::atomic<unsigned int> user_signals_seen{0};
std::mutex clock_mutex;

static_assert(std::atomic<unsigned int>::is_always_lock_free);

void signal_handler(int signal) {
    if (signal == SIGUSR1) {
        user_signals_seen.fetch_add(1, std::memory_order_relaxed);
    } else {
        signals_seen.fetch_add(1, std::memory_order_relaxed);
    }
}

void read_clocks(int index) {
    timespec ts;
    timeval tv;
    if (clock_gettime(CLOCK_REALTIME, &ts) || gettimeofday(&tv, nullptr)) {
        _exit(3);
    }
    const auto seconds = time(nullptr);
    clock_values[index].store(ts.tv_sec ^ tv.tv_sec ^ seconds, std::memory_order_relaxed);
    counters[index].fetch_add(1, std::memory_order_relaxed);
}

void work(int index, bool locking) {
    while (!stopping.load(std::memory_order_relaxed)) {
        if (locking) {
            std::lock_guard<std::mutex> lock(clock_mutex);
            read_clocks(index);
        } else {
            read_clocks(index);
        }
        if ((counters[index].load(std::memory_order_relaxed) & 255) == 0) {
            std::this_thread::yield();
        }
    }
}

int main(int argc, char **argv) {
    const rlimit limit{0, 0};
    setrlimit(RLIMIT_CORE, &limit);
    const bool locking = argc > 1 && std::string(argv[1]) == "mutex";
    const bool signal_mode = argc > 1 && std::string(argv[1]) == "signals";

    // A nonempty initial mask lets the regression verify exact restoration.
    sigset_t mask;
    sigemptyset(&mask);
    sigaddset(&mask, SIGUSR2);
    if (sigprocmask(SIG_BLOCK, &mask, nullptr)) return 2;
    struct sigaction action{};
    action.sa_handler = signal_handler;
    action.sa_flags = SA_RESTART;
    sigemptyset(&action.sa_mask);
    if (sigaction(SIGUSR1, &action, nullptr)) return 2;
    if (signal_mode) {
        if (sigaction(SIGALRM, &action, nullptr)) return 2;
        const itimerval timer{{0, 1000}, {0, 1000}};
        if (setitimer(ITIMER_REAL, &timer, nullptr)) return 2;
    }

    std::vector<std::thread> workers;
    for (int i = 1; i < 5; ++i) workers.emplace_back(work, i, locking);
    std::thread ticker([] {
        while (!stopping.load(std::memory_order_relaxed)) {
            std::this_thread::sleep_for(std::chrono::milliseconds(5));
            ticks.fetch_add(1, std::memory_order_relaxed);
        }
    });
    std::thread control([] {
        std::string command;
        while (std::getline(std::cin, command)) {
            if (command == "bye") break;
            if (command != "sample") continue;
            const auto wall = std::chrono::system_clock::now().time_since_epoch();
            std::printf("{\"wall\":%.9f,\"counts\":[", std::chrono::duration<double>(wall).count());
            for (int i = 0; i < 5; ++i) {
                std::printf("%s%llu", i ? "," : "", counters[i].load());
            }
            std::printf("],\"ticks\":%llu,\"signals\":%u,\"users\":%u}\n",
                        ticks.load(), signals_seen.load(), user_signals_seen.load());
            std::fflush(stdout);
        }
        stopping.store(true, std::memory_order_relaxed);
    });
    std::cout << "READY" << std::endl;
    // Unlike a probe blocked on stdin, the actual main thread stays busy.
    work(0, locking);
    for (auto &worker : workers) worker.join();
    ticker.join();
    control.join();
}
