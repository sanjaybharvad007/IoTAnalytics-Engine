package aggregation

import (
	"testing"
	"time"

	"iotanalytics/telematics-go"
)

func TestWindowEvictsOldReadings(t *testing.T) {
	w := NewWindow(10 * time.Second)
	base := time.Now()
	// old reading (20s ago) should be evicted when a fresh one arrives
	w.Add(telematics.Reading{VehicleID: "v1", Speed: 50, Timestamp: base.Add(-20 * time.Second)})
	w.Add(telematics.Reading{VehicleID: "v1", Speed: 60, Timestamp: base})
	if w.Size() != 1 {
		t.Errorf("expected old reading evicted, size=%d", w.Size())
	}
}

func TestAvgSpeedAndIdle(t *testing.T) {
	w := NewWindow(time.Minute)
	now := time.Now()
	w.Add(telematics.Reading{VehicleID: "v1", Speed: 40, EngineOn: true, Timestamp: now})
	w.Add(telematics.Reading{VehicleID: "v1", Speed: 60, EngineOn: true, Timestamp: now})
	w.Add(telematics.Reading{VehicleID: "v1", Speed: 0, EngineOn: true, Timestamp: now}) // idling
	if w.AvgSpeed() != 100.0/3 {
		t.Errorf("avg speed wrong: %v", w.AvgSpeed())
	}
	if w.IdleCount() != 1 {
		t.Errorf("expected 1 idle reading, got %d", w.IdleCount())
	}
}
