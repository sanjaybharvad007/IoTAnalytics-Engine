package aggregation

import (
	"math"
	"sync"
	"testing"
	"time"

	"iotanalytics/internal/geo"
	"iotanalytics/internal/telematics"
)

var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func rd(id string, offset time.Duration, speed float64) telematics.Reading {
	return telematics.Reading{VehicleID: id, Timestamp: base.Add(offset), Speed: speed,
		EngineOn: true, FuelLevel: 50, Lat: 37.3382, Lng: -121.8863}
}

func TestWindowEvictsOldReadings(t *testing.T) {
	w := NewWindow(10 * time.Second)
	w.Add(rd("v1", -20*time.Second, 50))
	w.Add(rd("v1", 0, 60))
	if w.Size() != 1 {
		t.Fatalf("expected old reading evicted, size=%d", w.Size())
	}
	if w.AvgSpeed() != 60 {
		t.Fatalf("avg should reflect only the retained reading, got %v", w.AvgSpeed())
	}
}

func TestWindowRejectsReadingsOlderThanSpan(t *testing.T) {
	w := NewWindow(10 * time.Second)
	w.Add(rd("v1", 0, 60))
	if w.Add(rd("v1", -30*time.Second, 10)) {
		t.Fatal("reading older than the window must be rejected")
	}
	if w.Size() != 1 {
		t.Fatalf("size=%d", w.Size())
	}
}

func TestWindowKeepsChronologicalOrderOnLateArrival(t *testing.T) {
	w := NewWindow(time.Minute)
	w.Add(rd("v1", 0, 10))
	w.Add(rd("v1", 4*time.Second, 30))
	w.Add(rd("v1", 2*time.Second, 20)) // arrives late
	w.mu.RLock()
	defer w.mu.RUnlock()
	for i := 1; i < len(w.readings); i++ {
		if w.readings[i].Timestamp.Before(w.readings[i-1].Timestamp) {
			t.Fatalf("readings out of order at %d", i)
		}
	}
	if w.readings[1].Speed != 20 {
		t.Fatalf("late reading not inserted in place: %+v", w.readings)
	}
}

func TestSnapshotMetrics(t *testing.T) {
	w := NewWindow(time.Minute)
	r1 := rd("v1", 0, 40)
	r1.FuelLevel = 60
	r2 := rd("v1", time.Second, 60)
	r2.FuelLevel = 59
	// Move 1 km due north for the second fix.
	p := geo.Offset(r1.Position(), 0, 1000)
	r2.Lat, r2.Lng = p.Lat, p.Lng
	r3 := rd("v1", 2*time.Second, 0) // idling
	r3.Lat, r3.Lng = p.Lat, p.Lng
	r3.FuelLevel = 58
	for _, r := range []telematics.Reading{r1, r2, r3} {
		w.Add(r)
	}

	s := w.Snapshot()
	if s.Samples != 3 || s.VehicleID != "v1" {
		t.Fatalf("bad snapshot header: %+v", s)
	}
	if math.Abs(s.AvgSpeed-100.0/3) > 1e-9 {
		t.Errorf("avg speed = %v", s.AvgSpeed)
	}
	if s.MaxSpeed != 60 {
		t.Errorf("max speed = %v", s.MaxSpeed)
	}
	if s.IdleCount != 1 || math.Abs(s.IdleRatio-1.0/3) > 1e-9 {
		t.Errorf("idle = %d ratio=%v", s.IdleCount, s.IdleRatio)
	}
	if math.Abs(s.DistanceM-1000) > 1 {
		t.Errorf("distance = %v, want ~1000", s.DistanceM)
	}
	if s.FuelUsedPct != 2 || s.FuelLevel != 58 {
		t.Errorf("fuel used=%v level=%v", s.FuelUsedPct, s.FuelLevel)
	}
}

func TestEmptyWindowSnapshotIsZero(t *testing.T) {
	if s := NewWindow(time.Minute).Snapshot(); s.Samples != 0 || s.AvgSpeed != 0 {
		t.Fatalf("expected zero snapshot, got %+v", s)
	}
}

func TestFleetRoutesPerVehicle(t *testing.T) {
	f := NewFleet(time.Minute)
	f.Add(rd("a", 0, 10))
	f.Add(rd("b", 0, 90))
	f.Add(rd("a", time.Second, 30))

	if f.Size() != 2 {
		t.Fatalf("fleet size=%d", f.Size())
	}
	a, _ := f.Snapshot("a")
	if a.Samples != 2 || a.AvgSpeed != 20 {
		t.Errorf("vehicle a: %+v", a)
	}
	if _, ok := f.Snapshot("zzz"); ok {
		t.Error("unknown vehicle should not have a snapshot")
	}
	snaps := f.Snapshots()
	if len(snaps) != 2 || snaps[0].VehicleID != "a" || snaps[1].VehicleID != "b" {
		t.Errorf("snapshots not sorted: %+v", snaps)
	}
}

func TestFleetSummarize(t *testing.T) {
	f := NewFleet(time.Minute)
	f.Add(rd("moving", 0, 50))
	f.Add(rd("idle", 0, 0))
	off := rd("off", 0, 0)
	off.EngineOn = false
	off.FuelLevel = 5
	f.Add(off)

	s := f.Summarize()
	if s.Vehicles != 3 || s.Moving != 1 || s.Idling != 1 || s.EngineOff != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.LowFuelCount != 1 {
		t.Errorf("low fuel count = %d", s.LowFuelCount)
	}
}

func TestFleetPrune(t *testing.T) {
	f := NewFleet(time.Minute)
	f.Add(rd("stale", -time.Hour, 10))
	f.Add(rd("fresh", 0, 10))
	gone := f.Prune(base, 10*time.Minute)
	if len(gone) != 1 || gone[0] != "stale" || f.Size() != 1 {
		t.Fatalf("prune removed %v, size=%d", gone, f.Size())
	}
}

func TestFleetConcurrentAdds(t *testing.T) {
	f := NewFleet(time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id := []string{"a", "b", "c"}[g%3]
			for i := 0; i < 500; i++ {
				f.Add(rd(id, time.Duration(i)*time.Millisecond, float64(i%80)))
				_ = f.Snapshots()
			}
		}(g)
	}
	wg.Wait()
	if f.Size() != 3 {
		t.Fatalf("size=%d", f.Size())
	}
}

func BenchmarkWindowAdd(b *testing.B) {
	w := NewWindow(time.Minute)
	for i := 0; i < b.N; i++ {
		w.Add(rd("v1", time.Duration(i)*time.Second, 50))
	}
}

func BenchmarkWindowSnapshot(b *testing.B) {
	w := NewWindow(time.Minute)
	for i := 0; i < 60; i++ {
		w.Add(rd("v1", time.Duration(i)*time.Second, 50))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = w.Snapshot()
	}
}
