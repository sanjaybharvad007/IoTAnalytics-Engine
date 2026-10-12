package alerts

import (
	"encoding/json"
	"testing"
	"time"

	"iotanalytics/internal/telematics"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func reading(speed, fuel float64, at time.Duration) telematics.Reading {
	return telematics.Reading{VehicleID: "v1", Speed: speed, FuelLevel: fuel, EngineOn: true,
		Timestamp: t0.Add(at), Lat: 37.33, Lng: -121.88}
}

func TestSpeedingEscalates(t *testing.T) {
	rule := Speeding(100)
	if rule.Evaluate(reading(80, 50, 0)) != nil {
		t.Fatal("should not alert under the limit")
	}
	if a := rule.Evaluate(reading(110, 50, 0)); a == nil || a.Severity != Warning {
		t.Fatalf("10%% over should be a warning, got %+v", a)
	}
	if a := rule.Evaluate(reading(130, 50, 0)); a == nil || a.Severity != Critical {
		t.Fatalf("30%% over should be critical, got %+v", a)
	}
}

func TestLowFuelEscalates(t *testing.T) {
	rule := LowFuel(15)
	if a := rule.Evaluate(reading(50, 10, 0)); a == nil || a.Severity != Warning {
		t.Fatalf("expected warning, got %+v", a)
	}
	if a := rule.Evaluate(reading(50, 3, 0)); a == nil || a.Severity != Critical {
		t.Fatalf("expected critical, got %+v", a)
	}
}

func TestThresholdRuleIsGeneric(t *testing.T) {
	coolant := Threshold("coolant_hot", func(r telematics.Reading) float64 { return r.Speed * 2 }, Above, 150, Warning)
	if coolant.Name() != "coolant_hot" {
		t.Fatal("name")
	}
	if coolant.Evaluate(reading(70, 50, 0)) != nil {
		t.Fatal("140 is not above 150")
	}
	a := coolant.Evaluate(reading(80, 50, 0))
	if a == nil || a.Value != 160 || a.Threshold != 150 {
		t.Fatalf("got %+v", a)
	}
	below := Threshold("x", func(r telematics.Reading) float64 { return r.FuelLevel }, Below, 20, Info)
	if below.Evaluate(reading(0, 10, 0)) == nil {
		t.Fatal("10 is below 20")
	}
}

func TestHarshBraking(t *testing.T) {
	rule := HarshBraking(20) // km/h per second
	if rule.Evaluate(reading(80, 50, 0)) != nil {
		t.Fatal("first reading has no history")
	}
	if rule.Evaluate(reading(70, 50, time.Second)) != nil {
		t.Fatal("-10 km/h/s is gentle")
	}
	a := rule.Evaluate(reading(20, 50, 2*time.Second))
	if a == nil || a.Severity != Critical || a.Value != 50 {
		t.Fatalf("expected critical harsh-braking alert, got %+v", a)
	}
	// A long gap between samples must not be interpreted as braking.
	rule.Evaluate(reading(100, 50, 3*time.Second))
	if rule.Evaluate(reading(0, 50, 5*time.Minute)) != nil {
		t.Fatal("gap > 30s should be ignored")
	}
	// Jitter between rapid samples: a 5 km/h drop in 0.2 s is a 25 km/h/s
	// rate but far too small an absolute change to be a braking event.
	rule.Evaluate(reading(60, 50, 10*time.Minute))
	if rule.Evaluate(reading(55, 50, 10*time.Minute+200*time.Millisecond)) != nil {
		t.Fatal("small absolute drop should be ignored regardless of rate")
	}
}

func TestExcessiveIdleFiresOncePerEpisode(t *testing.T) {
	rule := ExcessiveIdle(30 * time.Second)
	var fired int
	for s := 0; s <= 60; s++ {
		if rule.Evaluate(reading(0, 50, time.Duration(s)*time.Second)) != nil {
			fired++
		}
	}
	if fired != 1 {
		t.Fatalf("expected exactly one alert for a continuous idle episode, got %d", fired)
	}
	// Moving resets the episode; a new one fires again.
	rule.Evaluate(reading(40, 50, 61*time.Second))
	for s := 62; s <= 100; s++ {
		if rule.Evaluate(reading(0, 50, time.Duration(s)*time.Second)) != nil {
			fired++
		}
	}
	if fired != 2 {
		t.Fatalf("expected a second alert after moving, got %d", fired)
	}
}

func TestEngineMultipleRulesSortedBySeverity(t *testing.T) {
	e := NewEngine(WithRules(LowFuel(15), Speeding(100)))
	fired := e.Process(reading(130, 10, 0)) // critical speeding + warning fuel
	if len(fired) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(fired))
	}
	if fired[0].Kind != "speeding" || fired[0].Severity != Critical {
		t.Fatalf("most severe alert should come first: %+v", fired)
	}
	if got := e.Rules(); len(got) != 2 || got[0] != "low_fuel" {
		t.Fatalf("rules = %v", got)
	}
}

func TestEngineCooldownSuppressesRepeats(t *testing.T) {
	e := NewEngine(WithRules(Speeding(100)), WithCooldown(time.Minute))
	var count int
	for s := 0; s < 120; s++ { // two minutes of continuous speeding at 1 Hz
		count += len(e.Process(reading(110, 50, time.Duration(s)*time.Second)))
	}
	if count != 2 {
		t.Fatalf("expected one alert per cooldown period (2), got %d", count)
	}
}

func TestEngineCooldownAllowsEscalation(t *testing.T) {
	e := NewEngine(WithRules(Speeding(100)), WithCooldown(time.Minute))
	e.Process(reading(110, 50, 0)) // warning
	if len(e.Process(reading(112, 50, time.Second))) != 0 {
		t.Fatal("same-severity repeat should be suppressed")
	}
	if got := e.Process(reading(140, 50, 2*time.Second)); len(got) != 1 || got[0].Severity != Critical {
		t.Fatalf("escalation to critical must break through cooldown, got %+v", got)
	}
}

func TestEngineHistoryIsBoundedAndNewestFirst(t *testing.T) {
	e := NewEngine(WithRules(Speeding(100)), WithHistory(5))
	for s := 0; s < 12; s++ {
		e.Process(reading(110+float64(s), 50, time.Duration(s)*time.Second))
	}
	if e.Total() != 12 {
		t.Fatalf("total = %d", e.Total())
	}
	recent := e.Recent(0)
	if len(recent) != 5 {
		t.Fatalf("history should be capped at 5, got %d", len(recent))
	}
	if recent[0].Value != 121 || recent[4].Value != 117 {
		t.Fatalf("expected newest-first 121..117, got %v %v", recent[0].Value, recent[4].Value)
	}
	if got := e.Recent(2); len(got) != 2 || got[1].Value != 120 {
		t.Fatalf("Recent(2) = %+v", got)
	}
}

func TestSeverityJSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(Alert{Severity: Critical})
	if err != nil {
		t.Fatal(err)
	}
	var a Alert
	if err := json.Unmarshal(b, &a); err != nil || a.Severity != Critical {
		t.Fatalf("round trip failed: %s %v", b, err)
	}
	if _, err := ParseSeverity("bogus"); err == nil {
		t.Fatal("bogus severity should fail to parse")
	}
	if Critical <= Warning || Warning <= Info {
		t.Fatal("severity ordering broken")
	}
}

func BenchmarkEngineProcess(b *testing.B) {
	e := NewEngine(WithRules(Speeding(100), LowFuel(15), HarshBraking(20), ExcessiveIdle(time.Minute)),
		WithCooldown(time.Minute))
	for i := 0; i < b.N; i++ {
		e.Process(reading(float64(i%160), float64(i%100), time.Duration(i)*time.Second))
	}
}
