package docker

import (
	"context"
	"testing"
)

func TestStats_Argv_AndParse(t *testing.T) {
	// Realistic docker stats --format json output.
	payload := `{"BlockIO":"0B / 0B","CPUPerc":"12.34%","Container":"abc123","ID":"abc123","MemPerc":"4.82%","MemUsage":"12.34MiB / 256MiB","Name":"cmdr-proj-fb","NetIO":"1.23kB / 4.56kB","PIDs":"5"}`
	r := &recorder{stdout: []byte(payload + "\n")}
	c := &cmdClient{exec: r.fn(), OS: "linux"}

	stats, err := c.Stats(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	// argv: docker stats abc123 --no-stream --format json
	want := []string{"docker", "stats", "abc123", "--no-stream", "--format", "json"}
	if len(r.args) != len(want) {
		t.Fatalf("argv = %v; want %v", r.args, want)
	}
	for i, a := range want {
		if r.args[i] != a {
			t.Errorf("argv[%d] = %q; want %q", i, r.args[i], a)
		}
	}

	if stats.CPUPercent != 12.34 {
		t.Errorf("CPUPercent = %v; want 12.34", stats.CPUPercent)
	}
	// 12.34 * 1024 * 1024 = 12939526.something → int64
	v := 12.34 * 1024 * 1024
	wantUsed := int64(v)
	if stats.MemoryUsed != wantUsed {
		t.Errorf("MemoryUsed = %d; want %d", stats.MemoryUsed, wantUsed)
	}
	wantLimit := int64(256 * 1024 * 1024)
	if stats.MemoryLimit != wantLimit {
		t.Errorf("MemoryLimit = %d; want %d", stats.MemoryLimit, wantLimit)
	}
}

func TestStats_ZeroCPU(t *testing.T) {
	payload := `{"CPUPerc":"0.00%","MemUsage":"0B / 1GiB"}`
	r := &recorder{stdout: []byte(payload)}
	c := &cmdClient{exec: r.fn(), OS: "linux"}

	stats, err := c.Stats(context.Background(), "x")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.CPUPercent != 0.0 {
		t.Errorf("CPUPercent = %v", stats.CPUPercent)
	}
	if stats.MemoryUsed != 0 {
		t.Errorf("MemoryUsed = %d", stats.MemoryUsed)
	}
	if stats.MemoryLimit != 1024*1024*1024 {
		t.Errorf("MemoryLimit = %d; want 1GiB", stats.MemoryLimit)
	}
}

func TestStats_SIUnits(t *testing.T) {
	payload := `{"CPUPerc":"5%","MemUsage":"100MB / 1GB"}`
	r := &recorder{stdout: []byte(payload)}
	c := &cmdClient{exec: r.fn(), OS: "linux"}

	stats, err := c.Stats(context.Background(), "x")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.MemoryUsed != 100*1000*1000 {
		t.Errorf("MemoryUsed = %d; want 100MB SI", stats.MemoryUsed)
	}
	if stats.MemoryLimit != 1000*1000*1000 {
		t.Errorf("MemoryLimit = %d; want 1GB SI", stats.MemoryLimit)
	}
}

func TestStats_EmptyArg(t *testing.T) {
	c := &cmdClient{exec: (&recorder{}).fn(), OS: "linux"}
	if _, err := c.Stats(context.Background(), ""); err == nil {
		t.Error("expected error for empty id")
	}
}

func TestStats_BadJSON(t *testing.T) {
	r := &recorder{stdout: []byte("not json\n")}
	c := &cmdClient{exec: r.fn(), OS: "linux"}
	if _, err := c.Stats(context.Background(), "x"); err == nil {
		t.Error("expected parse error")
	}
}

func TestParseMemUsage_BadFormat(t *testing.T) {
	if _, _, err := parseMemUsage("12MiB"); err == nil {
		t.Error("expected error for missing slash")
	}
}
