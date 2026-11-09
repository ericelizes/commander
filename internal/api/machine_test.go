package api

import (
	"strings"
	"testing"
	"time"
)

func TestParseMeminfo(t *testing.T) {
	total, avail := parseMeminfo(strings.NewReader("MemTotal:       65808716 kB\nMemFree:        1000 kB\nMemAvailable:   54321000 kB\n"))
	if total != 65808716*1024 || avail != 54321000*1024 {
		t.Fatalf("total=%d avail=%d", total, avail)
	}
}

func TestParseVMStat(t *testing.T) {
	out := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:                               10.\nPages active:                            99.\nPages inactive:                           5.\nPages purgeable:                          1.\n"
	if got := parseVMStat(out); got != 16*16384 {
		t.Fatalf("avail = %d", got)
	}
}

func TestReadMachineKnowsThisComputer(t *testing.T) {
	m := readMachine()
	if m.Host == "" || m.CPUs == 0 || m.DiskTotalBytes == 0 || m.DiskFreeBytes > m.DiskTotalBytes {
		t.Fatalf("machine = %+v", m)
	}
	if m.MemTotalBytes != 0 && m.MemAvailBytes > m.MemTotalBytes {
		t.Fatalf("available memory above total: %+v", m)
	}
}

func TestSamplerKeepsTheLastWindowAsPercentages(t *testing.T) {
	m := &machineSampler{}
	for i := 0; i < machineSampleKeep+5; i++ {
		m.add(MachineSample{At: int64(i)})
	}
	got := m.samples()
	if len(got) != machineSampleKeep || got[0].At != 5 {
		t.Fatalf("kept %d, first at %d", len(got), got[0].At)
	}
	s := sampleOf(Machine{CPUs: 4, Load1: 6, MemTotalBytes: 100, MemAvailBytes: 25, DiskTotalBytes: 200, DiskFreeBytes: 150}, time.Unix(7, 0))
	if s.At != 7 || s.Mem != 75 || s.Disk != 25 || s.CPU != 100 {
		t.Fatalf("sample = %+v", s)
	}
}
