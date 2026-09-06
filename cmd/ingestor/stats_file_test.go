package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProcIORate_ZeroValuePrevSuppressesRate guards against the phantom-delta
// regression from #1169: when os.Open("/proc/self/io") fails, readProcSelfIO
// now returns a zero-value procIOSnapshot (ok=false, zero time.Time). This
// asserts procIORate returns nil so no inflated rate spike appears for the
// next successful read.
func TestProcIORate_ZeroValuePrevSuppressesRate(t *testing.T) {
	prev := procIOSnapshot{} // zero-value: ok=false, at=zero
	cur := procIOSnapshot{
		at:        time.Now(),
		readBytes: 1024 * 1024 * 100,
		ok:        true,
	}
	if got := procIORate(prev, cur, "2026-01-01T00:00:00Z"); got != nil {
		t.Fatalf("expected nil rate when prev is zero-value (os.Open failed), got %+v", got)
	}
}

// TestProcIORate_NormalPath asserts two valid snapshots produce a non-nil rate.
func TestProcIORate_NormalPath(t *testing.T) {
	base := time.Now()
	prev := procIOSnapshot{at: base, readBytes: 0, ok: true}
	cur := procIOSnapshot{at: base.Add(time.Second), readBytes: 1024, ok: true}
	got := procIORate(prev, cur, "2026-01-01T00:00:01Z")
	if got == nil {
		t.Fatal("expected non-nil rate for valid prev/cur pair")
	}
	if got.ReadBytesPerSec != 1024.0 {
		t.Errorf("ReadBytesPerSec: want 1024.0, got %v", got.ReadBytesPerSec)
	}
}

// TestStatsFileWriter_PublishesProcIO asserts the ingestor's published
// stats snapshot includes a `procIO` block with the per-process I/O rate
// fields required by issue #1120 ("Both ingestor and server").
func TestStatsFileWriter_PublishesProcIO(t *testing.T) {
	if _, err := os.Stat("/proc/self/io"); err != nil {
		t.Skip("skip: /proc/self/io unavailable on this host")
	}
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	StartStatsFileWriter(store, 50*time.Millisecond)

	// Wait for at least 2 ticks so the writer has had a chance to populate
	// procIO rates from a delta.
	deadline := time.Now().Add(3 * time.Second)
	var snap map[string]interface{}
	for time.Now().Before(deadline) {
		time.Sleep(75 * time.Millisecond)
		b, err := os.ReadFile(statsPath)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(b, &snap); err != nil {
			continue
		}
		if _, ok := snap["procIO"]; ok {
			break
		}
	}

	pio, ok := snap["procIO"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected procIO block in stats snapshot, got: %v", snap)
	}
	for _, field := range []string{"readBytesPerSec", "writeBytesPerSec", "cancelledWriteBytesPerSec", "syscallsRead", "syscallsWrite"} {
		v, present := pio[field]
		if !present {
			t.Errorf("procIO missing field %q", field)
			continue
		}
		// #1167 must-fix #5: assert the field actually decodes as a JSON
		// number, not just that the key exists. An empty PerfIOSample{}
		// substruct would still serialise the keys since the inner numeric
		// fields lack omitempty — without this Kind check the test would
		// silently pass on an empty struct regression.
		if _, isFloat := v.(float64); !isFloat {
			t.Errorf("procIO[%q] expected JSON number (float64), got %T (%v)", field, v, v)
		}
	}
}

// TestWriteStatsAtomic_SymlinkAtDestIsReplaced is a regression guardrail for
// #1170. The tmp side of writeStatsAtomic uses O_NOFOLLOW so a pre-planted
// symlink at path+".tmp" cannot redirect the write — but the rename target
// (`path` itself) is not protected by O_NOFOLLOW. Instead, os.Rename's
// semantics are relied upon: rename atomically replaces any existing entry
// at the destination, including a symlink, with the new regular file. The
// original symlink's target is never written through (because the write
// happened to the unrelated tmp file).
//
// This test pre-plants a symlink at `path` pointing to an unrelated target
// file and asserts:
//
//	(a) post-write, path is a regular file (not a symlink), and
//	(b) the original target's contents are unchanged.
//
// If a future refactor swaps os.Rename for something that follows the
// destination symlink (e.g. ioutil.WriteFile, or an open(path, O_WRONLY)
// without O_NOFOLLOW), this test will fail loudly.
func TestWriteStatsAtomic_SymlinkAtDestIsReplaced(t *testing.T) {
	dir := t.TempDir()

	// Unrelated target file with sentinel bytes. If writeStatsAtomic ever
	// followed the symlink at `path`, it would overwrite this file.
	target := filepath.Join(dir, "unrelated-target.bin")
	sentinel := []byte("DO-NOT-OVERWRITE-ME-#1170")
	if err := os.WriteFile(target, sentinel, 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	// Pre-plant a symlink at the destination path.
	path := filepath.Join(dir, "stats.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	payload := []byte(`{"sampledAt":"2026-01-01T00:00:00Z"}`)
	if err := writeStatsAtomic(path, payload); err != nil {
		t.Fatalf("writeStatsAtomic: %v", err)
	}

	// (a) post-write, path must NOT be a symlink.
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat path: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("post-write path is still a symlink (mode=%v); os.Rename should have atomically replaced it with a regular file", info.Mode())
	}
	if !info.Mode().IsRegular() {
		t.Errorf("post-write path is not a regular file (mode=%v)", info.Mode())
	}

	// Path now contains the new payload.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read path: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("path contents: want %q, got %q", payload, got)
	}

	// (b) the original symlink target must be unchanged.
	gotTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(gotTarget) != string(sentinel) {
		t.Errorf("symlink target was clobbered: want %q, got %q", sentinel, gotTarget)
	}
}
