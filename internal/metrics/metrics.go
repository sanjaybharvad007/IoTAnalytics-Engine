// Package metrics exposes engine telemetry in the Prometheus text exposition
// format without pulling in the Prometheus client library. Counters are
// maintained by consuming the engine's live event bus (so the exporter is
// just another subscriber), while gauges are sampled from the engine on each
// scrape.
package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"

	"iotanalytics/internal/alerts"
	"iotanalytics/internal/engine"
	"iotanalytics/internal/geofence"
	"iotanalytics/internal/storage"
)

// Exporter collects and serves metrics for one engine.
type Exporter struct {
	eng *engine.Engine

	mu          sync.Mutex
	alertsBy    map[[2]string]uint64 // {kind, severity} -> count
	geofenceBy  map[[2]string]uint64 // {zone, event} -> count
	eventsBy    map[string]uint64    // event type -> count seen on the bus
	stop        func()
	subscribed  bool
	subscribeMu sync.Once
}

// New creates an exporter and starts consuming the engine's event bus.
// Call Close to stop the background consumer.
func New(eng *engine.Engine) *Exporter {
	e := &Exporter{eng: eng,
		alertsBy: make(map[[2]string]uint64), geofenceBy: make(map[[2]string]uint64), eventsBy: make(map[string]uint64)}
	e.start()
	return e
}

func (e *Exporter) start() {
	e.subscribeMu.Do(func() {
		sub := e.eng.Subscribe(1024)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range sub.C {
				e.observe(ev)
			}
		}()
		e.stop = func() { sub.Close(); <-done }
		e.subscribed = true
	})
}

// Close stops the bus consumer.
func (e *Exporter) Close() {
	if e.stop != nil {
		e.stop()
	}
}

func (e *Exporter) observe(ev storage.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.eventsBy[string(ev.Type)]++
	switch ev.Type {
	case storage.TypeAlert:
		var a alerts.Alert
		if json.Unmarshal(ev.Payload, &a) == nil {
			e.alertsBy[[2]string{a.Kind, a.Severity.String()}]++
		}
	case storage.TypeGeofence:
		var t geofence.Transition
		if json.Unmarshal(ev.Payload, &t) == nil {
			e.geofenceBy[[2]string{t.Zone, string(t.Event)}]++
		}
	}
}

// ServeHTTP renders the metrics page.
func (e *Exporter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	e.Write(w)
}

// Write renders all metrics in exposition format to w.
func (e *Exporter) Write(w io.Writer) {
	p := &printer{w: w}
	stats := e.eng.Stats()
	fleet := e.eng.Fleet()
	summary := fleet.Summarize()

	p.gauge("iot_uptime_seconds", "Seconds since the engine started.", stats.UptimeSeconds)
	p.counter("iot_readings_ingested_total", "Readings accepted by the engine.", float64(stats.Ingested))
	p.counter("iot_readings_rejected_total", "Readings rejected by validation.", float64(stats.Rejected))
	p.counter("iot_alerts_raised_total", "Alerts admitted after cooldown de-duplication.", float64(stats.Alerts))
	p.counter("iot_geofence_transitions_total", "Geofence enter/exit transitions.", float64(stats.Transitions))

	e.mu.Lock()
	p.header("iot_alerts_by_kind_total", "Alerts by kind and severity.", "counter")
	for _, k := range sortedKeys2(e.alertsBy) {
		p.sample("iot_alerts_by_kind_total", labels{"kind": k[0], "severity": k[1]}, float64(e.alertsBy[k]))
	}
	p.header("iot_geofence_events_total", "Geofence transitions by zone and event.", "counter")
	for _, k := range sortedKeys2(e.geofenceBy) {
		p.sample("iot_geofence_events_total", labels{"zone": k[0], "event": k[1]}, float64(e.geofenceBy[k]))
	}
	p.header("iot_bus_events_total", "Events observed on the live bus by type.", "counter")
	for _, k := range sortedKeys(e.eventsBy) {
		p.sample("iot_bus_events_total", labels{"type": k}, float64(e.eventsBy[k]))
	}
	e.mu.Unlock()

	p.gauge("iot_vehicles_tracked", "Vehicles with an active sliding window.", float64(summary.Vehicles))
	p.gauge("iot_fleet_moving", "Vehicles currently moving.", float64(summary.Moving))
	p.gauge("iot_fleet_idling", "Vehicles idling (engine on, stationary).", float64(summary.Idling))
	p.gauge("iot_fleet_engine_off", "Vehicles with the engine off.", float64(summary.EngineOff))
	p.gauge("iot_fleet_low_fuel", "Vehicles below the low-fuel cutoff.", float64(summary.LowFuelCount))
	p.gauge("iot_fleet_avg_speed_kph", "Mean of per-vehicle rolling average speeds.", summary.AvgSpeed)
	p.gauge("iot_fleet_window_distance_meters", "Distance travelled by the fleet within the current windows.", summary.TotalDistM)

	p.header("iot_vehicle_speed_kph", "Rolling average speed per vehicle.", "gauge")
	p.header("iot_vehicle_fuel_percent", "Latest fuel level per vehicle.", "gauge")
	p.header("iot_vehicle_idle_ratio", "Share of engine-on samples spent idling per vehicle.", "gauge")
	for _, s := range fleet.Snapshots() {
		l := labels{"vehicle": s.VehicleID}
		p.sample("iot_vehicle_speed_kph", l, s.AvgSpeed)
		p.sample("iot_vehicle_fuel_percent", l, s.FuelLevel)
		p.sample("iot_vehicle_idle_ratio", l, s.IdleRatio)
	}

	p.header("iot_zone_occupancy", "Vehicles currently inside each geofence.", "gauge")
	occ := e.eng.Geofences().Occupancy()
	for _, z := range sortedKeysSlice(occ) {
		p.sample("iot_zone_occupancy", labels{"zone": z}, float64(len(occ[z])))
	}

	p.counter("iot_store_events_total", "Events in the durable log (replayed + appended).", float64(stats.StoredEvents))
	p.gauge("iot_store_last_seq", "Newest sequence number in the durable log.", float64(stats.LastSeq))
	p.gauge("iot_stream_subscribers", "Active live-stream subscriptions.", float64(stats.Subscribers))
	p.counter("iot_stream_dropped_total", "Messages dropped because a subscriber fell behind.", float64(stats.DroppedMessages))

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	p.gauge("go_goroutines", "Number of goroutines.", float64(runtime.NumGoroutine()))
	p.gauge("go_memstats_alloc_bytes", "Bytes of allocated heap objects.", float64(ms.Alloc))
	p.gauge("go_memstats_sys_bytes", "Bytes obtained from the OS.", float64(ms.Sys))
}

// ---- exposition helpers ----------------------------------------------------

type labels map[string]string

type printer struct{ w io.Writer }

func (p *printer) header(name, help, typ string) {
	fmt.Fprintf(p.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (p *printer) gauge(name, help string, v float64) {
	p.header(name, help, "gauge")
	p.sample(name, nil, v)
}

func (p *printer) counter(name, help string, v float64) {
	p.header(name, help, "counter")
	p.sample(name, nil, v)
}

func (p *printer) sample(name string, l labels, v float64) {
	if len(l) == 0 {
		fmt.Fprintf(p.w, "%s %s\n", name, format(v))
		return
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf(`%s="%s"`, k, escape(l[k]))
	}
	fmt.Fprintf(p.w, "%s{%s} %s\n", name, strings.Join(parts, ","), format(v))
}

func format(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

var escaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escape(s string) string { return escaper.Replace(s) }

func sortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysSlice(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys2(m map[[2]string]uint64) [][2]string {
	keys := make([][2]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	return keys
}
