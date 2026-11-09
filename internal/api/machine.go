package api

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Machine is how much room the computer cmdrd runs on has left, for the
// Settings view: memory, the home disk and CPU load. Zero means unknown.
type Machine struct {
	Host           string  `json:"host"`
	OS             string  `json:"os"`
	CPUs           int     `json:"cpus"`
	Load1          float64 `json:"load1"`
	MemTotalBytes  uint64  `json:"mem_total_bytes"`
	MemAvailBytes  uint64  `json:"mem_available_bytes"`
	DiskTotalBytes uint64  `json:"disk_total_bytes"`
	DiskFreeBytes  uint64  `json:"disk_free_bytes"`
}

func (s *Server) machineRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/machine", s.auth(http.HandlerFunc(s.serveMachine)))
}

func (s *Server) serveMachine(w http.ResponseWriter, _ *http.Request) {
	machineHistory.start()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "machine": readMachine(), "history": machineHistory.samples()})
}

// MachineSample is one reading as percentages, for the Settings line charts.
type MachineSample struct {
	At   int64   `json:"at"` // unix seconds
	Mem  float64 `json:"mem"`
	Disk float64 `json:"disk"`
	CPU  float64 `json:"cpu"`
}

const (
	machineSampleEvery = 2 * time.Second
	machineSampleKeep  = 900 // 30 minutes at 2s
)

// machineHistory is one sampler per cmdrd, started by New so the charts have
// the last 30 minutes the first time Settings opens.
var machineHistory = &machineSampler{}

type machineSampler struct {
	once sync.Once
	mu   sync.Mutex
	buf  []MachineSample
}

func (m *machineSampler) start() {
	m.once.Do(func() {
		go func() {
			for {
				m.add(sampleOf(readMachine(), time.Now()))
				time.Sleep(machineSampleEvery)
			}
		}()
	})
}

func (m *machineSampler) add(s MachineSample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf = append(m.buf, s)
	if len(m.buf) > machineSampleKeep {
		m.buf = append(m.buf[:0], m.buf[len(m.buf)-machineSampleKeep:]...)
	}
}

func (m *machineSampler) samples() []MachineSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MachineSample(nil), m.buf...)
}

func sampleOf(m Machine, at time.Time) MachineSample {
	pctOf := func(used, total float64) float64 {
		if total <= 0 {
			return 0
		}
		return min(100, max(0, used/total*100))
	}
	return MachineSample{
		At:   at.Unix(),
		Mem:  pctOf(float64(m.MemTotalBytes-min(m.MemAvailBytes, m.MemTotalBytes)), float64(m.MemTotalBytes)),
		Disk: pctOf(float64(m.DiskTotalBytes-min(m.DiskFreeBytes, m.DiskTotalBytes)), float64(m.DiskTotalBytes)),
		CPU:  pctOf(m.Load1, float64(m.CPUs)),
	}
}

func readMachine() Machine {
	m := Machine{Host: hostName(), OS: runtime.GOOS, CPUs: runtime.NumCPU()}
	if home, err := os.UserHomeDir(); err == nil {
		var st syscall.Statfs_t
		if syscall.Statfs(home, &st) == nil {
			bs := uint64(st.Bsize)
			m.DiskTotalBytes = st.Blocks * bs
			m.DiskFreeBytes = st.Bavail * bs
		}
	}
	switch runtime.GOOS {
	case "linux":
		if f, err := os.Open("/proc/meminfo"); err == nil {
			m.MemTotalBytes, m.MemAvailBytes = parseMeminfo(f)
			_ = f.Close()
		}
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			m.Load1 = firstFloat(string(b))
		}
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			m.MemTotalBytes, _ = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		}
		if out, err := exec.Command("vm_stat").Output(); err == nil {
			m.MemAvailBytes = parseVMStat(string(out))
		}
		if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
			m.Load1 = firstFloat(strings.Trim(strings.TrimSpace(string(out)), "{ }"))
		}
	}
	return m
}

// parseMeminfo reads MemTotal and MemAvailable (kB) from /proc/meminfo.
func parseMeminfo(r io.Reader) (total, avail uint64) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	return total, avail
}

// parseVMStat is the memory macOS can hand out without swapping: free,
// inactive and purgeable pages, at the page size vm_stat reports.
func parseVMStat(out string) uint64 {
	page := uint64(4096)
	var pages uint64
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "page size of "); i >= 0 {
			if n, err := strconv.ParseUint(strings.Fields(line[i+len("page size of "):])[0], 10, 64); err == nil {
				page = n
			}
			continue
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "Pages free", "Pages inactive", "Pages purgeable":
			n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(val), "."), 10, 64)
			pages += n
		}
	}
	return pages * page
}

func firstFloat(s string) float64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
