package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func open(t *testing.T, path string, opts ...Option) *Store {
	t.Helper()
	s, err := Open(path, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAppendAssignsSequenceAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.log")
	s1 := open(t, path)
	e1, err := s1.Append(TypeAlert, "v1", map[string]string{"kind": "speeding"})
	if err != nil || e1.Seq != 1 || e1.At.IsZero() {
		t.Fatalf("first append: %+v %v", e1, err)
	}
	e2, _ := s1.Append(TypeGeofence, "v1", "exit depot")
	if e2.Seq != 2 {
		t.Fatalf("seq should increment, got %d", e2.Seq)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Append(TypeAlert, "v1", nil); err != ErrClosed {
		t.Fatalf("append after close should fail with ErrClosed, got %v", err)
	}

	// Reopen: records and the sequence counter must survive.
	s2 := open(t, path)
	defer s2.Close()
	if s2.Count() != 2 || s2.LastSeq() != 2 || s2.Replayed != 2 {
		t.Fatalf("after reopen count=%d lastSeq=%d replayed=%d", s2.Count(), s2.LastSeq(), s2.Replayed)
	}
	e3, _ := s2.Append(TypeReading, "v2", 1)
	if e3.Seq != 3 {
		t.Fatalf("sequence must continue across restarts, got %d", e3.Seq)
	}
	got := s2.ByVehicle("v1", 0)
	if len(got) != 2 || got[0].Seq != 2 {
		t.Fatalf("ByVehicle = %+v", got)
	}
	var payload map[string]string
	if err := json.Unmarshal(got[1].Payload, &payload); err != nil || payload["kind"] != "speeding" {
		t.Fatalf("payload round-trip: %s %v", got[1].Payload, err)
	}
}

func TestRecoversFromTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.log")
	s := open(t, path)
	s.Append(TypeAlert, "v1", "a")
	s.Append(TypeAlert, "v1", "b")
	s.Close()

	// Simulate a crash mid-write: append half a JSON line with no newline.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"seq":3,"at":"2026-01-01T00:00:00Z","ty`)
	f.Close()

	s2 := open(t, path)
	defer s2.Close()
	if s2.Discarded != 1 || s2.Replayed != 2 || s2.LastSeq() != 2 {
		t.Fatalf("discarded=%d replayed=%d lastSeq=%d", s2.Discarded, s2.Replayed, s2.LastSeq())
	}
	e, err := s2.Append(TypeAlert, "v1", "c")
	if err != nil || e.Seq != 3 {
		t.Fatalf("append after recovery: %+v %v", e, err)
	}
	s2.Close()

	// The log must now be fully well-formed: every line parses.
	s3 := open(t, path)
	defer s3.Close()
	if s3.Discarded != 0 || s3.Replayed != 3 {
		t.Fatalf("log not clean after recovery: discarded=%d replayed=%d", s3.Discarded, s3.Replayed)
	}
}

func TestSinceSupportsResume(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ops.log"), WithSync(SyncNone))
	defer s.Close()
	for i := 0; i < 10; i++ {
		s.Append(TypeReading, "v1", i)
	}
	events, truncated := s.Since(7, 0)
	if truncated || len(events) != 3 || events[0].Seq != 8 || events[2].Seq != 10 {
		t.Fatalf("Since(7) = %d events (truncated=%v): %+v", len(events), truncated, events)
	}
	if events, _ := s.Since(0, 4); len(events) != 4 || events[3].Seq != 4 {
		t.Fatalf("limit not honoured: %+v", events)
	}
	if events, _ := s.Since(10, 0); len(events) != 0 {
		t.Fatalf("nothing newer than 10 expected, got %+v", events)
	}
}

func TestRetentionIsBoundedAndReportsGaps(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ops.log"), WithSync(SyncNone), WithRetention(5))
	defer s.Close()
	for i := 0; i < 12; i++ {
		s.Append(TypeAlert, "v1", i)
	}
	if s.Count() != 12 {
		t.Fatalf("count=%d", s.Count())
	}
	recent := s.Recent(0, "")
	if len(recent) != 5 || recent[0].Seq != 12 || recent[4].Seq != 8 {
		t.Fatalf("recent = %+v", recent)
	}
	events, truncated := s.Since(2, 0)
	if !truncated {
		t.Fatal("resuming from before retention must report truncation")
	}
	if len(events) != 5 || events[0].Seq != 8 {
		t.Fatalf("expected retained events 8..12, got %+v", events)
	}
	if _, truncated := s.Since(7, 0); truncated {
		t.Fatal("resuming from the retention boundary is not a gap")
	}
}

func TestRecentFiltersByType(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ops.log"), WithSync(SyncNone))
	defer s.Close()
	s.Append(TypeReading, "v1", 1)
	s.Append(TypeAlert, "v1", 2)
	s.Append(TypeReading, "v1", 3)
	if got := s.Recent(10, TypeAlert); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("Recent(alert) = %+v", got)
	}
	if got := s.Recent(1, ""); len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("Recent(1) = %+v", got)
	}
}

func TestConcurrentAppendsStaySequential(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ops.log"), WithSync(SyncNone))
	defer s.Close()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, err := s.Append(TypeReading, "v", i); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if s.Count() != 1600 || s.LastSeq() != 1600 {
		t.Fatalf("count=%d lastSeq=%d", s.Count(), s.LastSeq())
	}
	events, _ := s.Since(0, 0)
	for i, e := range events {
		if e.Seq != uint64(i+1) {
			t.Fatalf("gap or duplicate at %d: seq %d", i, e.Seq)
		}
	}
}

func TestOpenFailsOnUnwritablePath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "dir", "ops.log")); err == nil {
		t.Fatal("expected error for missing parent directory")
	}
}

func BenchmarkAppendNoSync(b *testing.B) {
	s, _ := Open(filepath.Join(b.TempDir(), "ops.log"), WithSync(SyncNone))
	defer s.Close()
	payload := map[string]any{"speed": 61.2, "fuel": 48.0}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Append(TypeReading, "v1", payload)
	}
}
