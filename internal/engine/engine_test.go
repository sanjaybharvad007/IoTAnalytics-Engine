package engine

import (
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"iotanalytics/internal/alerts"
	"iotanalytics/internal/geo"
	"iotanalytics/internal/geofence"
	"iotanalytics/internal/storage"
	"iotanalytics/internal/telematics"
)

var (
	t0    = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	depot = geofence.Circle{ZoneName: "depot", Center: geo.Point{Lat: 37.3382, Lng: -121.8863}, RadiusM: 1000}
)

func newEngine(t *testing.T, persistReadings bool) *Engine {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "ops.log"), storage.WithSync(storage.SyncNone))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e, err := New(Config{
		Store:           st,
		Zones:           []geofence.Zone{depot},
		Rules:           []alerts.Rule{alerts.Speeding(100), alerts.LowFuel(15)},
		Cooldown:        time.Minute,
		PersistReadings: persistReadings,
		Logger:          slog.New(slog.NewTextHandler(testWriter{t}, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

func reading(speed float64, at time.Duration, p geo.Point) telematics.Reading {
	return telematics.Reading{VehicleID: "van-7", Timestamp: t0.Add(at), Speed: speed, FuelLevel: 50,
		EngineOn: true, Lat: p.Lat, Lng: p.Lng}
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error without a store")
	}
}

func TestIngestRejectsInvalidReadings(t *testing.T) {
	e := newEngine(t, false)
	_, err := e.Ingest(telematics.Reading{VehicleID: "", Timestamp: t0})
	if !errors.Is(err, telematics.ErrMissingVehicle) {
		t.Fatalf("got %v", err)
	}
	if s := e.Stats(); s.Rejected != 1 || s.Ingested != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestIngestRunsFullPipeline(t *testing.T) {
	e := newEngine(t, false)
	sub := e.Subscribe(64)
	defer sub.Close()

	far := geo.Point{Lat: 37.50, Lng: -121.99}
	// Baseline inside the depot.
	if res, err := e.Ingest(reading(30, 0, depot.Center)); err != nil || len(res.Alerts) != 0 || len(res.Transitions) != 0 {
		t.Fatalf("baseline: %+v %v", res, err)
	}
	// Leave the depot while speeding: expect one alert and one exit transition.
	res, err := e.Ingest(reading(130, time.Minute, far))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Alerts) != 1 || res.Alerts[0].Kind != "speeding" || res.Alerts[0].Severity != alerts.Critical {
		t.Fatalf("alerts = %+v", res.Alerts)
	}
	if len(res.Transitions) != 1 || res.Transitions[0].Event != geofence.Exit {
		t.Fatalf("transitions = %+v", res.Transitions)
	}
	if res.Snapshot.Samples != 2 || res.Snapshot.MaxSpeed != 130 {
		t.Fatalf("snapshot = %+v", res.Snapshot)
	}

	// Durable store holds the alert and the transition but not the readings.
	if e.Store().Count() != 2 {
		t.Fatalf("stored = %d, want 2", e.Store().Count())
	}
	// The live bus carried everything: 2 readings (seq 0) + alert + geofence.
	var types []storage.EventType
	timeout := time.After(time.Second)
	for len(types) < 4 {
		select {
		case ev := <-sub.C:
			types = append(types, ev.Type)
			if ev.Type == storage.TypeReading && ev.Seq != 0 {
				t.Fatalf("unpersisted reading should have seq 0: %+v", ev)
			}
			if ev.Type != storage.TypeReading && ev.Seq == 0 {
				t.Fatalf("persisted event should carry a seq: %+v", ev)
			}
		case <-timeout:
			t.Fatalf("only received %v", types)
		}
	}
	if s := e.Stats(); s.Ingested != 2 || s.Alerts != 1 || s.Transitions != 1 || s.Vehicles != 1 || s.LastSeq != 2 {
		t.Fatalf("stats = %+v", s)
	}
	if len(e.Geofences().Inside("van-7")) != 0 {
		t.Fatal("vehicle should be outside the depot now")
	}
}

func TestPersistReadings(t *testing.T) {
	e := newEngine(t, true)
	e.Ingest(reading(30, 0, depot.Center))
	e.Ingest(reading(31, time.Second, depot.Center))
	if got := e.Store().Recent(10, storage.TypeReading); len(got) != 2 || got[0].Seq != 2 {
		t.Fatalf("readings not persisted: %+v", got)
	}
}

func TestIngestSurvivesStoreFailure(t *testing.T) {
	e := newEngine(t, false)
	e.Store().Close() // simulate the disk going away
	sub := e.Subscribe(8)
	defer sub.Close()
	res, err := e.Ingest(reading(130, 0, depot.Center))
	if err == nil || !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("expected a persistence error, got %v", err)
	}
	if len(res.Alerts) != 1 {
		t.Fatal("alerts must still be computed when persistence fails")
	}
	// The live stream still receives the alert (as an ephemeral event).
	got := 0
	deadline := time.After(time.Second)
	for got < 2 {
		select {
		case <-sub.C:
			got++
		case <-deadline:
			t.Fatalf("live stream stalled on store failure, got %d events", got)
		}
	}
}

func TestDefaultRules(t *testing.T) {
	st, _ := storage.Open(filepath.Join(t.TempDir(), "ops.log"))
	defer st.Close()
	e, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if rules := e.Alerts().Rules(); len(rules) != 4 || rules[0] != "speeding" {
		t.Fatalf("default rules = %v", rules)
	}
}
