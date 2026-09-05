// Package alerts implements a rule-based safety-alert engine over telematics.
// Field-ops platforms flag safety events (speeding, low fuel, harsh events) in
// real time so managers can act. Rules are composable and each returns an
// optional Alert.
package alerts

import (
	"fmt"

	"iotanalytics/telematics-go"
)

type Severity string

const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

type Alert struct {
	VehicleID string   `json:"vehicle_id"`
	Kind      string   `json:"kind"`
	Severity  Severity `json:"severity"`
	Message   string   `json:"message"`
}

// Rule inspects a reading and optionally returns an alert.
type Rule func(r telematics.Reading) *Alert

// SpeedingRule flags readings above a speed limit.
func SpeedingRule(limit float64) Rule {
	return func(r telematics.Reading) *Alert {
		if r.Speed > limit {
			return &Alert{r.VehicleID, "speeding", Warning,
				fmt.Sprintf("speed %.0f > limit %.0f", r.Speed, limit)}
		}
		return nil
	}
}

// LowFuelRule flags fuel below a threshold.
func LowFuelRule(minPct float64) Rule {
	return func(r telematics.Reading) *Alert {
		if r.FuelLevel < minPct {
			return &Alert{r.VehicleID, "low_fuel", Info,
				fmt.Sprintf("fuel %.0f%% below %.0f%%", r.FuelLevel, minPct)}
		}
		return nil
	}
}

// Engine is the alert engine: runs a set of rules over a stream of readings.
type Engine struct {
	rules  []Rule
	alerts []Alert
}

func NewEngine(rules ...Rule) *Engine { return &Engine{rules: rules} }

// Process runs every rule against a reading, collecting any alerts.
func (e *Engine) Process(r telematics.Reading) []Alert {
	var fired []Alert
	for _, rule := range e.rules {
		if a := rule(r); a != nil {
			e.alerts = append(e.alerts, *a)
			fired = append(fired, *a)
		}
	}
	return fired
}

func (e *Engine) All() []Alert { return e.alerts }
