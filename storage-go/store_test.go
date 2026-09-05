package storage

import (
	"path/filepath"
	"testing"
)

func TestAppendAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.log")
	s1, err := Open(path)
	if err != nil { t.Fatal(err) }
	s1.Append(Record{Type: "alert", Vehicle: "v1", Payload: "speeding"})
	s1.Append(Record{Type: "geofence", Vehicle: "v1", Payload: "exit depot"})
	s1.Close()

	// Reopen: records must survive.
	s2, err := Open(path)
	if err != nil { t.Fatal(err) }
	defer s2.Close()
	if s2.Count() != 2 {
		t.Errorf("expected 2 records after reopen, got %d", s2.Count())
	}
	if len(s2.ByVehicle("v1")) != 2 {
		t.Error("expected 2 records for v1")
	}
}
