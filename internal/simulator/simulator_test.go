package simulator

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"iotanalytics/internal/engine"
	"iotanalytics/internal/geo"
	"iotanalytics/internal/geofence"
	"iotanalytics/internal/storage"
	"iotanalytics/internal/telematics"
)

func TestFleetIsDeterministicAndValid(t *testing.T) {
	a, b := NewFleet(Options{Vehicles: 5, Seed: 42}), NewFleet(Options{Vehicles: 5, Seed: 42})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 300; i++ {
		ra, rb := a.Step(now), b.Step(now)
		if len(ra) != 5 {
			t.Fatalf("expected 5 readings, got %d", len(ra))
		}
		for j := range ra {
			if ra[j] != rb[j] {
				t.Fatalf("same seed must produce identical output at tick %d", i)
			}
			if err := ra[j].Validate(); err != nil {
				t.Fatalf("tick %d: invalid reading %+v: %v", i, ra[j], err)
			}
		}
		now = now.Add(time.Second)
	}
	if len(a.Vehicles()) != 5 || a.Vehicles()["van-02"] != "speeder" {
		t.Fatalf("profiles = %v", a.Vehicles())
	}
}

func TestVehiclesStayNearDepot(t *testing.T) {
	f := NewFleet(Options{Vehicles: 5, Seed: 7, RadiusM: 3000})
	now := time.Now()
	for i := 0; i < 1800; i++ {
		for _, r := range f.Step(now) {
			if d := geo.Distance(f.opts.Center, r.Position()); d > 3000+500 {
				t.Fatalf("%s wandered %.0f m from the depot", r.VehicleID, d)
			}
		}
		now = now.Add(time.Second)
	}
}

// The simulator must trigger every alert kind and both geofence events within
// a 10-minute run: this is what makes the demo and dashboard meaningful.
func TestProfilesExerciseEveryRule(t *testing.T) {
	st, _ := storage.Open(filepath.Join(t.TempDir(), "ops.log"), storage.WithSync(storage.SyncNone))
	defer st.Close()
	depot := geofence.Circle{ZoneName: "depot", Center: geo.Point{Lat: 37.3382, Lng: -121.8863}, RadiusM: 800}
	eng, _ := engine.New(engine.Config{Store: st, Zones: []geofence.Zone{depot},
		Logger: slog.New(slog.NewTextHandler(discard{}, nil))})

	f := NewFleet(Options{Vehicles: 5, Seed: 3})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kinds := map[string]bool{}
	events := map[geofence.Event]bool{}
	for i := 0; i < 600; i++ {
		for _, r := range f.Step(now) {
			res, err := eng.Ingest(r)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range res.Alerts {
				kinds[a.Kind] = true
			}
			for _, tr := range res.Transitions {
				events[tr.Event] = true
			}
		}
		now = now.Add(time.Second)
	}
	for _, k := range []string{"speeding", "low_fuel", "harsh_braking", "excessive_idle"} {
		if !kinds[k] {
			t.Errorf("rule %q never fired in a 10-minute simulation; fired: %v", k, kinds)
		}
	}
	if !events[geofence.Exit] || !events[geofence.Enter] {
		t.Errorf("expected both geofence events, got %v", events)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	f := NewFleet(Options{Vehicles: 2, Interval: 5 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	var n int
	err := Run(ctx, f, func(telematics.Reading) { n++ })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if n < 4 {
		t.Fatalf("expected several readings, got %d", n)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Scripted behaviour must be keyed to simulated time, not tick count: a fleet
// sampled 4x faster should still trigger every rule within the same wall time.
func TestBehaviourIsIndependentOfSampleRate(t *testing.T) {
	st, _ := storage.Open(filepath.Join(t.TempDir(), "ops.log"), storage.WithSync(storage.SyncNone))
	defer st.Close()
	eng, _ := engine.New(engine.Config{Store: st, Logger: slog.New(slog.NewTextHandler(discard{}, nil))})

	f := NewFleet(Options{Vehicles: 5, Seed: 3, Interval: 250 * time.Millisecond})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kinds := map[string]bool{}
	for i := 0; i < 600*4; i++ { // 10 simulated minutes at 4 Hz
		for _, r := range f.Step(now) {
			res, _ := eng.Ingest(r)
			for _, a := range res.Alerts {
				kinds[a.Kind] = true
			}
		}
		now = now.Add(250 * time.Millisecond)
	}
	for _, k := range []string{"speeding", "low_fuel", "harsh_braking", "excessive_idle"} {
		if !kinds[k] {
			t.Errorf("rule %q never fired at 4 Hz; fired: %v", k, kinds)
		}
	}
}
