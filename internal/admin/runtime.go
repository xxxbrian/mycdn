package admin

import (
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type runtimeInfo struct {
	Time       time.Time `json:"time"`
	GoVersion  string    `json:"go_version"`
	CPUs       int       `json:"cpus"`
	Goroutines int       `json:"goroutines"`
	HeapBytes  uint64    `json:"heap_bytes"`
	GoBytes    uint64    `json:"go_bytes"`
	CPUPercent float64   `json:"cpu_percent"`
	Uptime     float64   `json:"uptime_seconds"`
}

type runtimeMonitor struct {
	mu      sync.RWMutex
	start   time.Time
	last    time.Time
	lastCPU float64
	current runtimeInfo
	history []runtimeInfo
	stop    chan struct{}
	done    chan struct{}
}

func newRuntimeMonitor() *runtimeMonitor {
	now := time.Now()
	m := &runtimeMonitor{start: now, stop: make(chan struct{}), done: make(chan struct{})}
	m.sample()
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.sample()
			case <-m.stop:
				return
			}
		}
	}()
	return m
}

func (m *runtimeMonitor) snapshot() runtimeInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

func (m *runtimeMonitor) samples() []runtimeInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]runtimeInfo(nil), m.history...)
}

func (m *runtimeMonitor) close() {
	close(m.stop)
	<-m.done
}

func (m *runtimeMonitor) sample() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now, cpu := time.Now(), processCPU()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	var usage float64
	if !m.last.IsZero() {
		usage = 100 * (cpu - m.lastCPU) / now.Sub(m.last).Seconds()
	}
	m.last, m.lastCPU = now, cpu
	m.current = runtimeInfo{
		Time:      now,
		GoVersion: runtime.Version(), CPUs: runtime.GOMAXPROCS(0), Goroutines: runtime.NumGoroutine(),
		HeapBytes: memory.HeapAlloc, GoBytes: memory.Sys - memory.HeapReleased,
		CPUPercent: usage, Uptime: now.Sub(m.start).Seconds(),
	}
	// An hour of live diagnostics is enough for this panel; access analytics has
	// its own durable history. At most 720 small records, independent of traffic.
	if len(m.history) == 720 {
		copy(m.history, m.history[1:])
		m.history = m.history[:719]
	}
	m.history = append(m.history, m.current)
}

func processCPU() float64 {
	var usage unix.Rusage
	if unix.Getrusage(unix.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
}
