package main

import (
	"bytes"
	"log"
	"runtime/debug"
	"strings"
	"testing"
)

func TestApplyMemoryLimit_FromEnv(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "850MiB")
	// reset to a known state after test
	defer debug.SetMemoryLimit(-1)

	limit, source := applyMemoryLimit(512, true /* envSet */)
	if source != "env" {
		t.Fatalf("expected source=env, got %q", source)
	}
	// When env is set, our function must NOT override it; reported limit is 0.
	if limit != 0 {
		t.Fatalf("expected limit=0 (not set by us), got %d", limit)
	}
}

func TestApplyMemoryLimit_DerivedFromMaxMemoryMB(t *testing.T) {
	defer debug.SetMemoryLimit(-1)

	// maxMemoryMB=512 → 512 * 1.5 = 768 MiB = 768 * 1024 * 1024 bytes
	limit, source := applyMemoryLimit(512, false /* envSet */)
	if source != "derived" {
		t.Fatalf("expected source=derived, got %q", source)
	}
	want := int64(768) * 1024 * 1024
	if limit != want {
		t.Fatalf("expected limit=%d, got %d", want, limit)
	}
	// Verify it was actually set on the runtime
	cur := debug.SetMemoryLimit(-1)
	if cur != want {
		t.Fatalf("runtime memory limit not set: want=%d got=%d", want, cur)
	}
}

func TestApplyMemoryLimit_None(t *testing.T) {
	defer debug.SetMemoryLimit(-1)
	// Reset to "no limit" (math.MaxInt64) before test
	debug.SetMemoryLimit(int64(1<<63 - 1))

	limit, source := applyMemoryLimit(0, false)
	if source != "none" {
		t.Fatalf("expected source=none, got %q", source)
	}
	if limit != 0 {
		t.Fatalf("expected limit=0, got %d", limit)
	}
}

func TestMemlimitUnderprovisioned(t *testing.T) {
	cases := []struct {
		effective, cgroup int64
		want              bool
	}{
		{512, 1536, true},  // 512*2=1024 < 1536 → underprovisioned
		{768, 1536, false}, // 768*2=1536 == 1536 → not under (boundary)
		{1024, 1536, false},
		{0, 1536, false}, // no effective limit → skip
		{512, 0, false},  // no cgroup info → skip
	}
	for _, c := range cases {
		got := memlimitUnderprovisioned(c.effective, c.cgroup)
		if got != c.want {
			t.Errorf("memlimitUnderprovisioned(%d, %d) = %v, want %v", c.effective, c.cgroup, got, c.want)
		}
	}
}

// captureLog redirects the default logger to a buffer for the duration of f,
// then restores the previous writer. Returns captured output.
func captureLog(f func()) string {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	f()
	return buf.String()
}

// TestWarnIfMemlimitUnderprovisioned_EmitsWarning verifies the warning IS
// logged when the injected cgroup reader reports a container limit more than
// 2x larger than the effective GOMEMLIMIT.
func TestWarnIfMemlimitUnderprovisioned_EmitsWarning(t *testing.T) {
	defer debug.SetMemoryLimit(-1)
	// Effective: 512 MiB; container: 2048 MiB → 512*2=1024 < 2048 → warn
	debug.SetMemoryLimit(int64(512) * 1024 * 1024)

	orig := readCgroupMemoryMBFn
	readCgroupMemoryMBFn = func() int64 { return 2048 }
	defer func() { readCgroupMemoryMBFn = orig }()

	out := captureLog(func() {
		warnIfMemlimitUnderprovisioned(int64(512) * 1024 * 1024)
	})
	if !strings.Contains(out, "[memlimit] WARN") {
		t.Errorf("expected warning log, got: %q", out)
	}
}

// TestWarnIfMemlimitUnderprovisioned_NoWarnWhenAdequate verifies no warning
// when GOMEMLIMIT is >= 50% of the container limit.
func TestWarnIfMemlimitUnderprovisioned_NoWarnWhenAdequate(t *testing.T) {
	defer debug.SetMemoryLimit(-1)
	// Effective: 1024 MiB; container: 1536 MiB → 1024*2=2048 >= 1536 → no warn
	debug.SetMemoryLimit(int64(1024) * 1024 * 1024)

	orig := readCgroupMemoryMBFn
	readCgroupMemoryMBFn = func() int64 { return 1536 }
	defer func() { readCgroupMemoryMBFn = orig }()

	out := captureLog(func() {
		warnIfMemlimitUnderprovisioned(int64(1024) * 1024 * 1024)
	})
	if strings.Contains(out, "[memlimit] WARN") {
		t.Errorf("unexpected warning when limit is adequate: %q", out)
	}
}

// TestWarnIfMemlimitUnderprovisioned_NoCgroupNoLog verifies early exit when
// no cgroup info is available (non-Linux / non-container).
func TestWarnIfMemlimitUnderprovisioned_NoCgroupNoLog(t *testing.T) {
	defer debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(int64(512) * 1024 * 1024)

	orig := readCgroupMemoryMBFn
	readCgroupMemoryMBFn = func() int64 { return 0 }
	defer func() { readCgroupMemoryMBFn = orig }()

	out := captureLog(func() {
		warnIfMemlimitUnderprovisioned(int64(512) * 1024 * 1024)
	})
	if strings.Contains(out, "[memlimit] WARN") {
		t.Errorf("unexpected warning when cgroup unavailable: %q", out)
	}
}

// TestWarnIfMemlimitUnderprovisioned_NoneSource verifies that when no limit
// was configured (source="none", limitBytes=0), the function reads back
// math.MaxInt64 from the runtime and skips the warning.
func TestWarnIfMemlimitUnderprovisioned_NoneSource(t *testing.T) {
	defer debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(int64(1<<63 - 1)) // math.MaxInt64 = "no limit"

	orig := readCgroupMemoryMBFn
	readCgroupMemoryMBFn = func() int64 { return 2048 }
	defer func() { readCgroupMemoryMBFn = orig }()

	out := captureLog(func() {
		warnIfMemlimitUnderprovisioned(0) // source="none" passes limit=0
	})
	if strings.Contains(out, "[memlimit] WARN") {
		t.Errorf("unexpected warning when no limit configured: %q", out)
	}
}
