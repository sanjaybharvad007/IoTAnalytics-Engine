package alerts

import (
	"fmt"
	"sync"
	"time"

	"iotanalytics/internal/telematics"
)

// Rule inspects a reading and optionally raises an alert.
type Rule interface {
	// Name is the alert Kind the rule produces, e.g. "speeding".
	Name() string
	// Evaluate returns an alert for the reading, or nil when the rule is quiet.
	Evaluate(r telematics.Reading) *Alert
}

// RuleFunc adapts a plain function into a Rule.
type RuleFunc struct {
	Kind string
	Fn   func(r telematics.Reading) *Alert
}

// Name implements Rule.
func (f RuleFunc) Name() string { return f.Kind }

// Evaluate implements Rule.
func (f RuleFunc) Evaluate(r telematics.Reading) *Alert { return f.Fn(r) }

// Comparison selects how a Threshold rule compares a value with its limit.
type Comparison int

// Supported comparisons.
const (
	Above Comparison = iota // value > limit
	Below                   // value < limit
)

func (c Comparison) String() string {
	if c == Below {
		return "below"
	}
	return "above"
}

// Threshold is the generic building block: it extracts a numeric field from a
// reading with field, compares it to limit, and raises an alert of the given
// severity when the comparison holds. Speeding and LowFuel are specialisations
// with escalation; Threshold lets operators add new rules without code changes
// to the engine (e.g. tyre pressure, coolant temperature).
func Threshold(kind string, field func(telematics.Reading) float64, cmp Comparison, limit float64, sev Severity) Rule {
	return RuleFunc{Kind: kind, Fn: func(r telematics.Reading) *Alert {
		v := field(r)
		if (cmp == Above && v > limit) || (cmp == Below && v < limit) {
			return &Alert{VehicleID: r.VehicleID, Kind: kind, Severity: sev, Value: v, Threshold: limit,
				At: r.Timestamp, Message: fmt.Sprintf("%s %.1f %s limit %.1f", kind, v, cmp, limit)}
		}
		return nil
	}}
}

// Speeding flags readings above limitKph. Exceeding the limit by 25% or more
// escalates the alert from Warning to Critical.
func Speeding(limitKph float64) Rule {
	return RuleFunc{Kind: "speeding", Fn: func(r telematics.Reading) *Alert {
		if r.Speed <= limitKph {
			return nil
		}
		sev := Warning
		if r.Speed >= limitKph*1.25 {
			sev = Critical
		}
		return &Alert{VehicleID: r.VehicleID, Kind: "speeding", Severity: sev, Value: r.Speed,
			Threshold: limitKph, At: r.Timestamp,
			Message: fmt.Sprintf("speed %.0f km/h exceeds limit %.0f km/h", r.Speed, limitKph)}
	}}
}

// LowFuel flags fuel below minPct (Warning) and below CriticalFuelPct (Critical).
func LowFuel(minPct float64) Rule {
	return RuleFunc{Kind: "low_fuel", Fn: func(r telematics.Reading) *Alert {
		if r.FuelLevel >= minPct {
			return nil
		}
		sev := Warning
		if r.FuelLevel < CriticalFuelPct {
			sev = Critical
		}
		return &Alert{VehicleID: r.VehicleID, Kind: "low_fuel", Severity: sev, Value: r.FuelLevel,
			Threshold: minPct, At: r.Timestamp,
			Message: fmt.Sprintf("fuel %.0f%% below %.0f%%", r.FuelLevel, minPct)}
	}}
}

// CriticalFuelPct is the level at which LowFuel escalates to Critical.
const CriticalFuelPct = 5.0

// HarshBraking is a stateful rule: it remembers each vehicle's previous reading
// and raises a Critical alert when deceleration exceeds maxDecel (km/h per
// second). Harsh events are the strongest single predictor of collision risk
// in driver-safety scoring.
func HarshBraking(maxDecelKphPerSec float64) Rule {
	return &harshBraking{limit: maxDecelKphPerSec, last: make(map[string]telematics.Reading)}
}

type harshBraking struct {
	limit float64
	mu    sync.Mutex
	last  map[string]telematics.Reading
}

func (h *harshBraking) Name() string { return "harsh_braking" }

func (h *harshBraking) Evaluate(r telematics.Reading) *Alert {
	h.mu.Lock()
	prev, ok := h.last[r.VehicleID]
	h.last[r.VehicleID] = r
	h.mu.Unlock()
	if !ok {
		return nil
	}
	dt := r.Timestamp.Sub(prev.Timestamp).Seconds()
	if dt <= 0 || dt > 30 { // gap too large to infer acceleration
		return nil
	}
	decel := (prev.Speed - r.Speed) / dt
	if decel <= h.limit {
		return nil
	}
	return &Alert{VehicleID: r.VehicleID, Kind: "harsh_braking", Severity: Critical, Value: decel,
		Threshold: h.limit, At: r.Timestamp,
		Message: fmt.Sprintf("decelerated %.0f km/h in %.1fs (%.1f km/h/s)", prev.Speed-r.Speed, dt, decel)}
}

// ExcessiveIdle is a stateful rule that tracks how long each vehicle has been
// idling (engine on, stationary) and raises a Warning once the continuous idle
// time exceeds maxIdle. The alert fires once per idle episode.
func ExcessiveIdle(maxIdle time.Duration) Rule {
	return &excessiveIdle{limit: maxIdle, since: make(map[string]time.Time), fired: make(map[string]bool)}
}

type excessiveIdle struct {
	limit time.Duration
	mu    sync.Mutex
	since map[string]time.Time
	fired map[string]bool
}

func (e *excessiveIdle) Name() string { return "excessive_idle" }

func (e *excessiveIdle) Evaluate(r telematics.Reading) *Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !r.IsIdling() {
		delete(e.since, r.VehicleID)
		delete(e.fired, r.VehicleID)
		return nil
	}
	start, ok := e.since[r.VehicleID]
	if !ok {
		e.since[r.VehicleID] = r.Timestamp
		return nil
	}
	idle := r.Timestamp.Sub(start)
	if idle < e.limit || e.fired[r.VehicleID] {
		return nil
	}
	e.fired[r.VehicleID] = true
	return &Alert{VehicleID: r.VehicleID, Kind: "excessive_idle", Severity: Warning,
		Value: idle.Seconds(), Threshold: e.limit.Seconds(), At: r.Timestamp,
		Message: fmt.Sprintf("idling for %s (limit %s)", idle.Round(time.Second), e.limit)}
}
