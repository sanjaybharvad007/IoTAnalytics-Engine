// Package alerts implements a composable, rule-based safety-alert engine over
// telemetry. Field-operations platforms flag safety and efficiency events
// (speeding, harsh braking, low fuel, excessive idling) in real time so that
// dispatchers can act. Rules are small, independently testable units; the
// Engine composes them, de-duplicates noisy repeats with a per-vehicle
// cooldown, and keeps a bounded history of what fired.
package alerts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Severity ranks how urgently an alert needs attention. The numeric order is
// meaningful: Critical > Warning > Info.
type Severity int

const (
	Info Severity = iota + 1
	Warning
	Critical
)

var severityNames = map[Severity]string{Info: "info", Warning: "warning", Critical: "critical"}

// String returns the lower-case wire name of the severity.
func (s Severity) String() string {
	if n, ok := severityNames[s]; ok {
		return n
	}
	return fmt.Sprintf("severity(%d)", int(s))
}

// MarshalJSON encodes the severity as its string name ("critical"), which is
// what the TypeScript client and dashboards consume.
func (s Severity) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// UnmarshalJSON accepts the string names produced by MarshalJSON.
func (s *Severity) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	return s.parse(name)
}

// ParseSeverity converts a wire name into a Severity.
func ParseSeverity(name string) (Severity, error) {
	var s Severity
	err := s.parse(name)
	return s, err
}

func (s *Severity) parse(name string) error {
	for sev, n := range severityNames {
		if n == strings.ToLower(strings.TrimSpace(name)) {
			*s = sev
			return nil
		}
	}
	return fmt.Errorf("alerts: unknown severity %q", name)
}

// Alert is a single safety or efficiency event raised for a vehicle.
type Alert struct {
	VehicleID string    `json:"vehicle_id"`
	Kind      string    `json:"kind"`
	Severity  Severity  `json:"severity"`
	Message   string    `json:"message"`
	Value     float64   `json:"value"`     // the measurement that triggered the rule
	Threshold float64   `json:"threshold"` // the limit it was compared against
	At        time.Time `json:"at"`
}

// Key identifies the (vehicle, kind) pair used for cooldown de-duplication.
func (a Alert) Key() string { return a.VehicleID + "/" + a.Kind }
