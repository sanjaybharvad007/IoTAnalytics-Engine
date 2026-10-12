// Package engine wires the analytics components into a single ingest
// pipeline:
//
//	reading -> validate -> sliding window -> alert rules -> geofence monitor
//	                                   \__ durable store __/
//	                                            |
//	                                       live event bus
//
// Everything downstream (HTTP API, SSE stream, metrics, dashboards) consumes
// the engine through Ingest, the accessor methods and Subscribe.
package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"iotanalytics/internal/aggregation"
	"iotanalytics/internal/alerts"
	"iotanalytics/internal/bus"
	"iotanalytics/internal/geofence"
	"iotanalytics/internal/storage"
	"iotanalytics/internal/telematics"
)

// Event is the unit published on the live bus and streamed to clients. It is
// the stored event shape; readings that are not persisted carry Seq 0.
type Event = storage.Event

// Config describes an engine.
type Config struct {
	// WindowSpan is the per-vehicle sliding window (default 5m).
	WindowSpan time.Duration
	// Cooldown suppresses repeat alerts of the same kind per vehicle
	// (default 60s). A negative value disables de-duplication entirely.
	Cooldown time.Duration
	// Rules are the alert rules to evaluate; nil selects a sensible default set.
	Rules []alerts.Rule
	// Zones are the geofences to monitor.
	Zones []geofence.Zone
	// Store persists alerts and geofence transitions (and readings when
	// PersistReadings is set). Required.
	Store *storage.Store
	// PersistReadings writes every accepted reading to the store. Off by
	// default: readings are high-volume and are streamed live regardless.
	PersistReadings bool
	// Logger receives structured diagnostics; nil uses slog.Default().
	Logger *slog.Logger
}

// DefaultRules returns the rule set used when Config.Rules is nil.
func DefaultRules(speedLimitKph float64) []alerts.Rule {
	return []alerts.Rule{
		alerts.Speeding(speedLimitKph),
		alerts.LowFuel(15),
		alerts.HarshBraking(25),
		alerts.ExcessiveIdle(5 * time.Minute),
	}
}

// Engine is the composed analytics pipeline. It is safe for concurrent use.
type Engine struct {
	fleet   *aggregation.Fleet
	rules   *alerts.Engine
	fences  *geofence.Monitor
	store   *storage.Store
	bus     *bus.Bus[Event]
	log     *slog.Logger
	persist bool
	started time.Time

	ingested    atomic.Uint64
	rejected    atomic.Uint64
	alertCount  atomic.Uint64
	transitions atomic.Uint64
}

// New builds an engine from cfg. It returns an error only for a missing store.
func New(cfg Config) (*Engine, error) {
	if cfg.Store == nil {
		return nil, errors.New("engine: Config.Store is required")
	}
	if cfg.WindowSpan <= 0 {
		cfg.WindowSpan = 5 * time.Minute
	}
	if cfg.Cooldown < 0 {
		cfg.Cooldown = 0
	} else if cfg.Cooldown == 0 {
		cfg.Cooldown = time.Minute
	}
	if cfg.Rules == nil {
		cfg.Rules = DefaultRules(100)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Engine{
		fleet:   aggregation.NewFleet(cfg.WindowSpan),
		rules:   alerts.NewEngine(alerts.WithRules(cfg.Rules...), alerts.WithCooldown(cfg.Cooldown)),
		fences:  geofence.NewMonitor(cfg.Zones...),
		store:   cfg.Store,
		bus:     bus.New[Event](),
		log:     cfg.Logger,
		persist: cfg.PersistReadings,
		started: time.Now(),
	}, nil
}

// Result reports what a single reading produced.
type Result struct {
	Snapshot    aggregation.Snapshot  `json:"snapshot"`
	Alerts      []alerts.Alert        `json:"alerts"`
	Transitions []geofence.Transition `json:"transitions"`
}

// Ingest runs one reading through the full pipeline. Validation failures are
// returned as errors wrapping the telematics sentinel errors; downstream
// persistence failures are logged and returned but do not stop the live
// stream, so a disk hiccup degrades durability rather than availability.
func (e *Engine) Ingest(r telematics.Reading) (Result, error) {
	if err := r.Validate(); err != nil {
		e.rejected.Add(1)
		return Result{}, err
	}
	e.ingested.Add(1)

	var res Result
	e.fleet.Add(r)
	res.Snapshot, _ = e.fleet.Snapshot(r.VehicleID)
	res.Alerts = e.rules.Process(r)
	if r.HasFix() {
		res.Transitions = e.fences.Update(r.VehicleID, r.Position(), r.Timestamp)
	}
	e.alertCount.Add(uint64(len(res.Alerts)))
	e.transitions.Add(uint64(len(res.Transitions)))

	var errs []error
	// Readings: persisted optionally, streamed always.
	if e.persist {
		if ev, err := e.store.Append(storage.TypeReading, r.VehicleID, r); err != nil {
			errs = append(errs, err)
		} else {
			e.bus.Publish(ev)
		}
	} else {
		e.bus.Publish(e.ephemeral(storage.TypeReading, r.VehicleID, r.Timestamp, r))
	}
	for _, a := range res.Alerts {
		ev, err := e.store.Append(storage.TypeAlert, a.VehicleID, a)
		if err != nil {
			errs = append(errs, err)
			ev = e.ephemeral(storage.TypeAlert, a.VehicleID, a.At, a)
		}
		e.bus.Publish(ev)
		e.log.Warn("alert", "vehicle", a.VehicleID, "kind", a.Kind, "severity", a.Severity.String(), "msg", a.Message)
	}
	for _, t := range res.Transitions {
		ev, err := e.store.Append(storage.TypeGeofence, t.VehicleID, t)
		if err != nil {
			errs = append(errs, err)
			ev = e.ephemeral(storage.TypeGeofence, t.VehicleID, t.At, t)
		}
		e.bus.Publish(ev)
		e.log.Info("geofence", "vehicle", t.VehicleID, "zone", t.Zone, "event", t.Event)
	}
	if len(errs) > 0 {
		err := fmt.Errorf("%w: %w", ErrPersist, errors.Join(errs...))
		e.log.Error("persistence degraded", "err", err)
		return res, err
	}
	return res, nil
}

// ErrPersist wraps storage failures returned by Ingest. The reading was still
// analysed and streamed; only durability was affected.
var ErrPersist = errors.New("engine: persistence degraded")

// ephemeral builds a non-durable event (Seq 0) for live streaming.
func (e *Engine) ephemeral(typ storage.EventType, vehicle string, at time.Time, payload any) Event {
	raw, _ := json.Marshal(payload)
	return Event{At: at.UTC(), Type: typ, VehicleID: vehicle, Payload: raw}
}

// Subscribe returns a live subscription to every event the engine emits.
// Callers must Close it.
func (e *Engine) Subscribe(buffer int) *bus.Subscription[Event] { return e.bus.Subscribe(buffer) }

// Fleet exposes the per-vehicle sliding windows.
func (e *Engine) Fleet() *aggregation.Fleet { return e.fleet }

// Alerts exposes the alert engine (rules, recent history).
func (e *Engine) Alerts() *alerts.Engine { return e.rules }

// Geofences exposes the geofence monitor (zones, occupancy).
func (e *Engine) Geofences() *geofence.Monitor { return e.fences }

// Store exposes the durable event log.
func (e *Engine) Store() *storage.Store { return e.store }

// Stats is a point-in-time view of engine counters.
type Stats struct {
	UptimeSeconds   float64  `json:"uptime_seconds"`
	Ingested        uint64   `json:"readings_ingested"`
	Rejected        uint64   `json:"readings_rejected"`
	Alerts          uint64   `json:"alerts_raised"`
	Transitions     uint64   `json:"geofence_transitions"`
	Vehicles        int      `json:"vehicles_tracked"`
	StoredEvents    uint64   `json:"stored_events"`
	LastSeq         uint64   `json:"last_seq"`
	Subscribers     int      `json:"stream_subscribers"`
	DroppedMessages uint64   `json:"stream_dropped"`
	Rules           []string `json:"rules"`
}

// Stats returns the current counters.
func (e *Engine) Stats() Stats {
	return Stats{
		UptimeSeconds:   time.Since(e.started).Seconds(),
		Ingested:        e.ingested.Load(),
		Rejected:        e.rejected.Load(),
		Alerts:          e.alertCount.Load(),
		Transitions:     e.transitions.Load(),
		Vehicles:        e.fleet.Size(),
		StoredEvents:    e.store.Count(),
		LastSeq:         e.store.LastSeq(),
		Subscribers:     e.bus.Subscribers(),
		DroppedMessages: e.bus.Dropped(),
		Rules:           e.rules.Rules(),
	}
}
