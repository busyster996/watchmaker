use std::io::{self, BufRead, Write};
use std::sync::{atomic::{AtomicU64, Ordering}, Arc, Condvar, Mutex};
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

fn wall() -> f64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs_f64()
}

fn main() {
    let counter = Arc::new(AtomicU64::new(0));
    let origin = Instant::now();
    for _ in 0..4 {
        let count = Arc::clone(&counter);
        thread::spawn(move || loop {
            for _ in 0..100 { let _ = SystemTime::now(); }
            count.fetch_add(100, Ordering::Relaxed);
            thread::sleep(Duration::from_millis(1));
        });
    }
    println!("READY");
    io::stdout().flush().unwrap();
    for line in io::stdin().lock().lines() {
        match line.unwrap().as_str() {
            "sample" => println!("{{\"wall\":{{\"SystemTime\":{:.9}}},\"uptime\":{:.9},\"counter\":{}}}", wall(), origin.elapsed().as_secs_f64(), counter.load(Ordering::Relaxed)),
            "timers" => {
                let start = Instant::now();
                thread::sleep(Duration::from_millis(20));
                let sleep = start.elapsed().as_secs_f64();
                let mutex = Mutex::new(false);
                let cond = Condvar::new();
                let start = Instant::now();
                let _result = cond.wait_timeout(mutex.lock().unwrap(), Duration::from_millis(20)).unwrap();
                println!("{{\"relative\":{{\"thread.sleep\":{:.9},\"Condvar.wait_timeout\":{:.9}}}}}", sleep, start.elapsed().as_secs_f64());
            },
            "bye" => return,
            _ => {},
        }
        io::stdout().flush().unwrap();
    }
}
