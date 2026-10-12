package geofence

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"iotanalytics/internal/geo"
)

var (
	depotCenter = geo.Point{Lat: 37.3382, Lng: -121.8863} // downtown San Jose
	depot       = Circle{ZoneName: "depot", Center: depotCenter, RadiusM: 1000}
	farAway     = geo.Point{Lat: 37.50, Lng: -121.99}
	t0          = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// 4 km square (2 km half-width) around the depot.
	square = Polygon{ZoneName: "yard", Vertices: []geo.Point{
		geo.Offset(depotCenter, 315, 2828), geo.Offset(depotCenter, 45, 2828),
		geo.Offset(depotCenter, 135, 2828), geo.Offset(depotCenter, 225, 2828),
	}}
)

func TestCircleContains(t *testing.T) {
	if !depot.Contains(geo.Point{Lat: 37.3385, Lng: -121.8865}) {
		t.Error("nearby point should be inside")
	}
	if depot.Contains(farAway) {
		t.Error("far point should be outside")
	}
	edge := geo.Offset(depotCenter, 90, 999)
	if !depot.Contains(edge) {
		t.Error("point 1 m inside the radius should be inside")
	}
}

func TestPolygonContains(t *testing.T) {
	if !square.Contains(depotCenter) {
		t.Error("centre of the square should be inside")
	}
	if !square.Contains(geo.Offset(depotCenter, 0, 1900)) {
		t.Error("1.9 km north is still inside a 2 km half-width square")
	}
	if square.Contains(geo.Offset(depotCenter, 0, 2100)) {
		t.Error("2.1 km north is outside")
	}
	if square.Contains(farAway) {
		t.Error("far point should be outside")
	}
	if (Polygon{ZoneName: "degenerate", Vertices: square.Vertices[:2]}).Contains(depotCenter) {
		t.Error("a polygon with fewer than 3 vertices contains nothing")
	}
}

func TestEnterExitTransitions(t *testing.T) {
	m := NewMonitor(depot)

	if tr := m.Update("van-1", farAway, t0); len(tr) != 0 {
		t.Fatalf("first reading sets baseline only, got %+v", tr)
	}
	tr := m.Update("van-1", depotCenter, t0.Add(time.Minute))
	if len(tr) != 1 || tr[0].Event != Enter || tr[0].Zone != "depot" {
		t.Fatalf("expected enter, got %+v", tr)
	}
	if tr := m.Update("van-1", geo.Offset(depotCenter, 90, 50), t0.Add(2*time.Minute)); len(tr) != 0 {
		t.Fatalf("no transition while staying inside, got %+v", tr)
	}
	if inside := m.Inside("van-1"); len(inside) != 1 || inside[0] != "depot" {
		t.Fatalf("Inside = %v", inside)
	}
	tr = m.Update("van-1", farAway, t0.Add(10*time.Minute))
	if len(tr) != 1 || tr[0].Event != Exit {
		t.Fatalf("expected exit, got %+v", tr)
	}
	if tr[0].Dwell != 9*time.Minute {
		t.Fatalf("dwell = %v, want 9m", tr[0].Dwell)
	}
}

func TestStartingInsideDoesNotEmitEnter(t *testing.T) {
	m := NewMonitor(depot)
	if tr := m.Update("van-2", depotCenter, t0); len(tr) != 0 {
		t.Fatalf("vehicle starting inside should not 'enter', got %+v", tr)
	}
	if tr := m.Update("van-2", farAway, t0.Add(time.Minute)); len(tr) != 1 || tr[0].Event != Exit {
		t.Fatalf("expected exit, got %+v", tr)
	}
}

func TestMultipleZonesAndOccupancy(t *testing.T) {
	m := NewMonitor(depot, square)
	m.Update("a", farAway, t0)
	m.Update("b", farAway, t0)
	tr := m.Update("a", depotCenter, t0.Add(time.Second))
	if len(tr) != 2 {
		t.Fatalf("entering both overlapping zones should emit 2 transitions, got %+v", tr)
	}
	// 1.5 km north: outside the 1 km circle but inside the 2 km half-width square.
	m.Update("b", geo.Offset(depotCenter, 0, 1500), t0.Add(time.Second))
	occ := m.Occupancy()
	if len(occ["depot"]) != 1 || occ["depot"][0] != "a" {
		t.Errorf("depot occupancy = %v", occ["depot"])
	}
	if len(occ["yard"]) != 2 {
		t.Errorf("yard occupancy = %v", occ["yard"])
	}
	m.Forget("a")
	if len(m.Occupancy()["depot"]) != 0 {
		t.Error("forgotten vehicle should leave occupancy")
	}
}

func TestTransitionJSONUsesSeconds(t *testing.T) {
	tr := Transition{Zone: "depot", VehicleID: "v", Event: Exit, At: t0, Dwell: 90 * time.Second}
	b, err := json.Marshal(tr)
	if err != nil || !strings.Contains(string(b), `"dwell_seconds":90`) {
		t.Fatalf("json = %s (%v)", b, err)
	}
	var back Transition
	if err := json.Unmarshal(b, &back); err != nil || back.Dwell != 90*time.Second || back.Event != Exit {
		t.Fatalf("round trip = %+v (%v)", back, err)
	}
}

func TestParseZones(t *testing.T) {
	data := []byte(`[
	  {"name":"depot","type":"circle","center":{"lat":37.3382,"lng":-121.8863},"radius_m":1000},
	  {"name":"yard","type":"polygon","vertices":[{"lat":37.34,"lng":-121.9},{"lat":37.34,"lng":-121.87},{"lat":37.33,"lng":-121.88}]}
	]`)
	zones, err := Parse(data)
	if err != nil || len(zones) != 2 {
		t.Fatalf("parse: %v (%d zones)", err, len(zones))
	}
	if zones[0].Spec().Type != "circle" || zones[1].Spec().Type != "polygon" {
		t.Fatalf("specs = %+v %+v", zones[0].Spec(), zones[1].Spec())
	}

	bad := []string{
		`[{"name":"","type":"circle","center":{"lat":0,"lng":0},"radius_m":10}]`,
		`[{"name":"x","type":"circle","radius_m":10}]`,
		`[{"name":"x","type":"circle","center":{"lat":99,"lng":0},"radius_m":10}]`,
		`[{"name":"x","type":"polygon","vertices":[{"lat":0,"lng":0}]}]`,
		`[{"name":"x","type":"hexagon"}]`,
		`[{"name":"dup","type":"circle","center":{"lat":0,"lng":0},"radius_m":10},{"name":"dup","type":"circle","center":{"lat":0,"lng":0},"radius_m":10}]`,
	}
	for _, b := range bad {
		if _, err := Parse([]byte(b)); !errors.Is(err, ErrInvalidZone) {
			t.Errorf("expected ErrInvalidZone for %s, got %v", b, err)
		}
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("malformed JSON should fail")
	}
}

func TestMonitorConcurrentUpdates(t *testing.T) {
	m := NewMonitor(depot, square)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id := []string{"a", "b", "c", "d"}[g%4]
			for i := 0; i < 300; i++ {
				p := depotCenter
				if i%2 == 0 {
					p = farAway
				}
				m.Update(id, p, t0.Add(time.Duration(i)*time.Second))
				_ = m.Occupancy()
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkPolygonContains(b *testing.B) {
	p := geo.Offset(depotCenter, 30, 500)
	for i := 0; i < b.N; i++ {
		square.Contains(p)
	}
}
