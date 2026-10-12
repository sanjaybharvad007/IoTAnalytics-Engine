package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

type fixture struct {
	eng *engine.Engine
	srv *httptest.Server
}

func setup(t *testing.T) *fixture {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "ops.log"), storage.WithSync(storage.SyncNone))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.New(engine.Config{Store: st, Zones: []geofence.Zone{depot}, Logger: quiet,
		Rules: []alerts.Rule{alerts.Speeding(100), alerts.LowFuel(15)}, Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(eng, WithLogger(quiet), WithHeartbeat(50*time.Millisecond)))
	t.Cleanup(srv.Close)
	return &fixture{eng: eng, srv: srv}
}

func reading(id string, speed float64, at time.Duration, p geo.Point) telematics.Reading {
	return telematics.Reading{VehicleID: id, Timestamp: t0.Add(at), Speed: speed, FuelLevel: 50,
		EngineOn: true, Lat: p.Lat, Lng: p.Lng}
}

func (f *fixture) post(t *testing.T, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	switch b := body.(type) {
	case string:
		buf.WriteString(b)
	default:
		json.NewEncoder(&buf).Encode(b)
	}
	resp, err := http.Post(f.srv.URL+path, "application/json", &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (f *fixture) get(t *testing.T, path string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp
}

func TestHealth(t *testing.T) {
	f := setup(t)
	var body map[string]any
	if resp := f.get(t, "/healthz", &body); resp.StatusCode != 200 || body["status"] != "ok" {
		t.Fatalf("health = %d %v", resp.StatusCode, body)
	}
	if resp := f.get(t, "/nope", nil); resp.StatusCode != 404 {
		t.Fatalf("unknown route = %d", resp.StatusCode)
	}
	if resp := f.get(t, "/healthz", nil); resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
}

func TestIngestSingleAndBatch(t *testing.T) {
	f := setup(t)
	resp, data := f.post(t, "/api/v1/telemetry", reading("van-1", 30, 0, depot.Center))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("single ingest = %d %s", resp.StatusCode, data)
	}
	batch := []telematics.Reading{
		reading("van-1", 130, time.Minute, far), // speeding + exits depot
		reading("van-2", 40, time.Minute, far),
		{VehicleID: "", Timestamp: t0}, // invalid
	}
	resp, data = f.post(t, "/api/v1/telemetry", batch)
	var out IngestResponse
	json.Unmarshal(data, &out)
	if resp.StatusCode != http.StatusAccepted || out.Accepted != 2 || out.Rejected != 1 {
		t.Fatalf("batch = %d %+v", resp.StatusCode, out)
	}
	if len(out.Errors) != 1 || out.Errors[0].Index != 2 {
		t.Fatalf("errors = %+v", out.Errors)
	}
	if len(out.Alerts) != 1 || out.Alerts[0].Kind != "speeding" || len(out.Transitions) != 1 {
		t.Fatalf("alerts=%+v transitions=%+v", out.Alerts, out.Transitions)
	}
}

func TestIngestRejectsBadInput(t *testing.T) {
	f := setup(t)
	cases := map[string]string{
		"empty":          "",
		"not json":       "{",
		"bad array":      "[1,2",
		"invalid values": `{"vehicle_id":"x","timestamp":"2026-01-01T00:00:00Z","speed":-5}`,
	}
	for name, body := range cases {
		if resp, _ := f.post(t, "/api/v1/telemetry", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
	}
	// Oversized body.
	big := strings.Repeat(" ", 2<<20)
	if resp, _ := f.post(t, "/api/v1/telemetry", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status %d", resp.StatusCode)
	}
	// Wrong method.
	resp, _ := http.Get(f.srv.URL + "/api/v1/telemetry")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET telemetry = %d", resp.StatusCode)
	}
}

func TestQueries(t *testing.T) {
	f := setup(t)
	f.eng.Ingest(reading("van-1", 30, 0, depot.Center))
	f.eng.Ingest(reading("van-1", 130, time.Minute, far))
	f.eng.Ingest(reading("van-2", 50, time.Minute, depot.Center))

	var fleet FleetResponse
	f.get(t, "/api/v1/fleet", &fleet)
	if fleet.Summary.Vehicles != 2 || len(fleet.Vehicles) != 2 || fleet.Vehicles[0].VehicleID != "van-1" {
		t.Fatalf("fleet = %+v", fleet)
	}

	var v VehicleResponse
	f.get(t, "/api/v1/vehicles/van-1", &v)
	if v.Snapshot.MaxSpeed != 130 || len(v.Zones) != 0 || len(v.Events) != 2 {
		t.Fatalf("vehicle = %+v", v)
	}
	f.get(t, "/api/v1/vehicles/van-2", &v)
	if len(v.Zones) != 1 || v.Zones[0] != "depot" {
		t.Fatalf("van-2 should be inside the depot: %+v", v)
	}
	if resp := f.get(t, "/api/v1/vehicles/ghost", nil); resp.StatusCode != 404 {
		t.Fatalf("unknown vehicle = %d", resp.StatusCode)
	}

	var al []alerts.Alert
	f.get(t, "/api/v1/alerts?severity=critical", &al)
	if len(al) != 1 || al[0].Kind != "speeding" {
		t.Fatalf("alerts = %+v", al)
	}
	if resp := f.get(t, "/api/v1/alerts?severity=bogus", nil); resp.StatusCode != 400 {
		t.Fatalf("bad severity = %d", resp.StatusCode)
	}

	var zones ZonesResponse
	f.get(t, "/api/v1/zones", &zones)
	if len(zones.Zones) != 1 || zones.Zones[0].Type != "circle" || zones.Occupancy["depot"][0] != "van-2" {
		t.Fatalf("zones = %+v", zones)
	}

	var events EventsResponse
	f.get(t, "/api/v1/events?after=1", &events)
	if len(events.Events) != 1 || events.Events[0].Seq != 2 || events.LastSeq != 2 {
		t.Fatalf("events = %+v", events)
	}
	f.get(t, "/api/v1/events?type=alert", &events)
	if len(events.Events) != 1 || events.Events[0].Type != storage.TypeAlert {
		t.Fatalf("typed events = %+v", events)
	}
	if resp := f.get(t, "/api/v1/events?after=x", nil); resp.StatusCode != 400 {
		t.Fatalf("bad after = %d", resp.StatusCode)
	}

	var stats engine.Stats
	f.get(t, "/api/v1/stats", &stats)
	if stats.Ingested != 3 || stats.Alerts != 1 || stats.Transitions != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestCORSPreflight(t *testing.T) {
	f := setup(t)
	req, _ := http.NewRequest(http.MethodOptions, f.srv.URL+"/api/v1/telemetry", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight = %d %v", resp.StatusCode, resp.Header)
	}
}

// sseEvent is one parsed SSE frame.
type sseEvent struct {
	id, event, data string
}

// readSSE parses frames from an event stream until n events are read or the
// context expires. Comment lines (heartbeats) are ignored.
func readSSE(ctx context.Context, body io.Reader, n int) ([]sseEvent, error) {
	var out []sseEvent
	sc := bufio.NewScanner(body)
	var cur sseEvent
	ch := make(chan error, 1)
	go func() {
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.data != "" || cur.event != "" {
					out = append(out, cur)
					cur = sseEvent{}
					if len(out) >= n {
						ch <- nil
						return
					}
				}
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "id: "):
				cur.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				cur.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				cur.data = line[6:]
			}
		}
		ch <- fmt.Errorf("stream ended after %d events: %w", len(out), sc.Err())
	}()
	select {
	case err := <-ch:
		return out, err
	case <-ctx.Done():
		return out, fmt.Errorf("timeout after %d events", len(out))
	}
}

func TestStreamDeliversLiveEvents(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/v1/stream?types=alert,geofence", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	// Give the handler a moment to subscribe, then produce events.
	time.Sleep(50 * time.Millisecond)
	f.eng.Ingest(reading("van-1", 30, 0, depot.Center)) // reading only: filtered out
	f.eng.Ingest(reading("van-1", 130, time.Minute, far))

	events, err := readSSE(ctx, resp.Body, 2)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].event != "alert" || events[0].id != "1" || events[1].event != "geofence" || events[1].id != "2" {
		t.Fatalf("events = %+v", events)
	}
	var ev storage.Event
	if err := json.Unmarshal([]byte(events[0].data), &ev); err != nil || ev.VehicleID != "van-1" {
		t.Fatalf("payload = %s (%v)", events[0].data, err)
	}
}

func TestStreamResumesFromLastEventID(t *testing.T) {
	f := setup(t)
	f.eng.Ingest(reading("van-1", 30, 0, depot.Center))
	f.eng.Ingest(reading("van-1", 130, time.Minute, far))           // seq 1 alert, seq 2 geofence exit
	f.eng.Ingest(reading("van-1", 140, 2*time.Minute, far))         // seq 3 alert (cooldown expired)
	f.eng.Ingest(reading("van-1", 30, 3*time.Minute, depot.Center)) // seq 4 geofence enter
	if f.eng.Store().LastSeq() != 4 {
		t.Fatalf("expected 4 stored events, got %d", f.eng.Store().LastSeq())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/api/v1/stream", nil)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	events, err := readSSE(ctx, resp.Body, 3)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].id != "2" || events[1].id != "3" || events[2].id != "4" {
		t.Fatalf("expected replay of seq 2, 3 and 4, got %+v", events)
	}
	if events[0].event != "geofence" || events[1].event != "alert" {
		t.Fatalf("unexpected event types: %+v", events)
	}
}

func TestStreamReportsGapWhenRetentionExceeded(t *testing.T) {
	st, _ := storage.Open(filepath.Join(t.TempDir(), "ops.log"), storage.WithSync(storage.SyncNone), storage.WithRetention(2))
	defer st.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, _ := engine.New(engine.Config{Store: st, Logger: quiet, Rules: []alerts.Rule{alerts.Speeding(100)}, Cooldown: -1})
	srv := httptest.NewServer(New(eng, WithLogger(quiet)))
	defer srv.Close()
	for i := 0; i < 5; i++ {
		eng.Ingest(reading("van-1", 120, time.Duration(i)*time.Second, far))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/stream?after=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events, err := readSSE(ctx, resp.Body, 3)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].event != "gap" || events[1].id != "4" || events[2].id != "5" {
		t.Fatalf("expected gap then seq 4,5; got %+v", events)
	}
}

func TestStreamRejectsBadResumeID(t *testing.T) {
	f := setup(t)
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/v1/stream", nil)
	req.Header.Set("Last-Event-ID", "abc")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestExtraHandlersMountAlongsideAPI(t *testing.T) {
	st, _ := storage.Open(filepath.Join(t.TempDir(), "ops.log"), storage.WithSync(storage.SyncNone))
	defer st.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, _ := engine.New(engine.Config{Store: st, Logger: quiet})
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("<html>dashboard</html>"))
	})
	metricsH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("up 1\n")) })
	srv := httptest.NewServer(New(eng, WithLogger(quiet), WithExtraHandler("GET /", root), WithExtraHandler("GET /metrics", metricsH)))
	defer srv.Close()

	for path, want := range map[string]int{"/": 200, "/metrics": 200, "/api/v1/fleet": 200, "/api/v1/nope": 404, "/other": 404} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}
