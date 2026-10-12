package metrics

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"iotanalytics/internal/alerts"
	"iotanalytics/internal/engine"
	"iotanalytics/internal/geo"
	"iotanalytics/internal/geofence"
	"iotanalytics/internal/storage"
	"iotanalytics/internal/telematics"
)

var (
	t0    = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	depot = geofence.Circle{ZoneName: "depot", Center: geo.Point{Lat: 37.3382, Lng: -121.8863}, RadiusM: 1000}
	far   = geo.Point{Lat: 37.50, Lng: -121.99}
)

func newEngine(t *testing.T) *engine.Engine {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "ops.log"), storage.WithSync(storage.SyncNone))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng, err := engine.New(engine.Config{Store: st, Zones: []geofence.Zone{depot},
		Rules: []alerts.Rule{alerts.Speeding(100)}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func reading(id string, speed float64, at time.Duration, p geo.Point) telematics.Reading {
	return telematics.Reading{VehicleID: id, Timestamp: t0.Add(at), Speed: speed, FuelLevel: 50,
		EngineOn: true, Lat: p.Lat, Lng: p.Lng}
}

// line matches a Prometheus sample line and captures its value.
func value(t *testing.T, body, sample string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(sample) + ` (\S+)$`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("sample %q not found in:\n%s", sample, body)
	}
	return m[1]
}

func TestExporterCountsAndGauges(t *testing.T) {
	eng := newEngine(t)
	exp := New(eng)
	defer exp.Close()

	eng.Ingest(reading("van-1", 30, 0, depot.Center))
	eng.Ingest(reading("van-1", 130, time.Minute, far)) // critical speeding + depot exit
	eng.Ingest(reading("van-2", 40, time.Minute, depot.Center))
	eng.Ingest(telematics.Reading{VehicleID: "bad"}) // rejected

	// The bus consumer is asynchronous; wait for it to catch up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		exp.mu.Lock()
		n := exp.eventsBy["alert"]
		exp.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	rec := httptest.NewRecorder()
	exp.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()

	checks := map[string]string{
		"iot_readings_ingested_total":                                   "3",
		"iot_readings_rejected_total":                                   "1",
		"iot_alerts_raised_total":                                       "1",
		`iot_alerts_by_kind_total{kind="speeding",severity="critical"}`: "1",
		`iot_geofence_events_total{event="exit",zone="depot"}`:          "1",
		`iot_bus_events_total{type="reading"}`:                          "3",
		"iot_vehicles_tracked":                                          "2",
		`iot_vehicle_speed_kph{vehicle="van-2"}`:                        "40",
		`iot_zone_occupancy{zone="depot"}`:                              "1",
		"iot_store_last_seq":                                            "2",
	}
	for sample, want := range checks {
		if got := value(t, body, sample); got != want {
			t.Errorf("%s = %s, want %s", sample, got, want)
		}
	}
	if !strings.Contains(body, "# TYPE iot_alerts_by_kind_total counter") {
		t.Error("missing TYPE header")
	}
	if !strings.Contains(body, "go_goroutines ") {
		t.Error("missing runtime metrics")
	}
}

func TestLabelEscaping(t *testing.T) {
	var sb strings.Builder
	p := &printer{w: &sb}
	p.sample("x", labels{"zone": `a"b\c` + "\n"}, 1.5)
	if got := sb.String(); got != `x{zone="a\"b\\c\n"} 1.5`+"\n" {
		t.Fatalf("got %q", got)
	}
}

func TestCloseIsSafe(t *testing.T) {
	exp := New(newEngine(t))
	exp.Close()
	exp.Close()
}
