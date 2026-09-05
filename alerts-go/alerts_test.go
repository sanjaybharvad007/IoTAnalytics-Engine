package alerts

import (
	"testing"
	"time"

	"iotanalytics/telematics-go"
)

func reading(speed, fuel float64) telematics.Reading {
	return telematics.Reading{VehicleID: "v1", Speed: speed, FuelLevel: fuel,
		EngineOn: true, Timestamp: time.Now()}
}

func TestSpeedingRule(t *testing.T) {
	e := NewEngine(SpeedingRule(100))
	if len(e.Process(reading(120, 50))) != 1 {
		t.Error("expected a speeding alert")
	}
	if len(e.Process(reading(80, 50))) != 0 {
		t.Error("should not alert under the limit")
	}
}

func TestLowFuelRule(t *testing.T) {
	e := NewEngine(LowFuelRule(15))
	if len(e.Process(reading(50, 10)) ) != 1 {
		t.Error("expected a low-fuel alert")
	}
}

func TestMultipleRulesFire(t *testing.T) {
	e := NewEngine(SpeedingRule(100), LowFuelRule(15))
	fired := e.Process(reading(120, 5)) // both speeding AND low fuel
	if len(fired) != 2 {
		t.Errorf("expected 2 alerts, got %d", len(fired))
	}
}
