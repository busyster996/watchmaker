package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

var extraWall = map[string]func() float64{}

func main() {
	origin := time.Now()
	var counter atomic.Uint64
	for i := 0; i < 4; i++ {
		go func() {
			for {
				for j := 0; j < 100; j++ {
					_ = time.Now()
				}
				counter.Add(100)
				time.Sleep(time.Millisecond)
			}
		}()
	}
	fmt.Println("READY")
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		switch scanner.Text() {
		case "sample":
			wall := map[string]float64{"time.Now": float64(time.Now().UnixNano()) / 1e9, "time.Now.UTC": float64(time.Now().UTC().UnixNano()) / 1e9}
			for name, read := range extraWall {
				wall[name] = read()
			}
			_ = encoder.Encode(map[string]any{"wall": wall, "uptime": time.Since(origin).Seconds(), "counter": counter.Load()})
		case "timers":
			relative := map[string]float64{}
			start := time.Now()
			time.Sleep(20 * time.Millisecond)
			relative["time.Sleep"] = time.Since(start).Seconds()
			start = time.Now()
			<-time.After(20 * time.Millisecond)
			relative["time.After"] = time.Since(start).Seconds()
			start = time.Now()
			ticker := time.NewTicker(20 * time.Millisecond)
			<-ticker.C
			ticker.Stop()
			relative["time.Ticker"] = time.Since(start).Seconds()
			start = time.Now()
			<-time.NewTimer(time.Until(time.Now().Add(20 * time.Millisecond))).C
			relative["wall_deadline_to_duration"] = time.Since(start).Seconds()
			_ = encoder.Encode(map[string]any{"relative": relative})
		case "bye":
			return
		}
	}
}
