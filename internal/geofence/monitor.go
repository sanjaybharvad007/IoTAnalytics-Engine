package geofence

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"iotanalytics/internal/geo"
)

// Event is the kind of boundary crossing.
type Event string

// Boundary-crossing events.
const (
	Enter Event = "enter"
	Exit  Event = "exit"
)

// Transition describes a vehicle crossing a zone boundary.
type Transition struct {
	Zone      string    `json:"zone"`
	VehicleID string    `json:"vehicle_id"`
	Event     Event     `json:"event"`
	Point     geo.Point `json:"point"`
	At        time.Time `json:"at"`
	// Dwell is how long the vehicle spent inside the zone; set on Exit only.
	// It is serialised as fractional seconds under "dwell_seconds".
	Dwell time.Duration `json:"-"`
}

type transitionJSON struct {
	Zone         string    `json:"zone"`
	VehicleID    string    `json:"vehicle_id"`
	Event        Event     `json:"event"`
	Point        geo.Point `json:"point"`
	At           time.Time `json:"at"`
	DwellSeconds float64   `json:"dwell_seconds,omitempty"`
}

// MarshalJSON encodes Dwell as seconds rather than Go's nanosecond integer.
func (t Transition) MarshalJSON() ([]byte, error) {
	return json.Marshal(transitionJSON{t.Zone, t.VehicleID, t.Event, t.Point, t.At, t.Dwell.Seconds()})
}

// UnmarshalJSON is the inverse of MarshalJSON.
func (t *Transition) UnmarshalJSON(b []byte) error {
	var j transitionJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*t = Transition{Zone: j.Zone, VehicleID: j.VehicleID, Event: j.Event, Point: j.Point, At: j.At,
		Dwell: time.Duration(j.DwellSeconds * float64(time.Second))}
	return nil
}

type membership struct {
	since time.Time
}

// Monitor tracks each vehicle's inside/outside state per zone so it can emit a
// Transition only on the crossing itself rather than on every reading.
// It is safe for concurrent use.
type Monitor struct {
	mu     sync.RWMutex
	zones  []Zone
	inside map[string]map[string]membership // vehicle -> zone -> membership
}

// NewMonitor creates a monitor over the given zones.
func NewMonitor(zones ...Zone) *Monitor {
	return &Monitor{zones: zones, inside: make(map[string]map[string]membership)}
}

// Zones returns the serialisable specs of all monitored zones.
func (m *Monitor) Zones() []Spec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	specs := make([]Spec, len(m.zones))
	for i, z := range m.zones {
		specs[i] = z.Spec()
	}
	return specs
}

// AddZone registers an additional zone at runtime.
func (m *Monitor) AddZone(z Zone) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.zones = append(m.zones, z)
}

// Update feeds a vehicle's new position and returns any enter/exit transitions.
// The first observation of a vehicle establishes its baseline state without
// emitting events, so a vehicle that starts inside a depot does not "enter" it.
func (m *Monitor) Update(vehicleID string, p geo.Point, at time.Time) []Transition {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, seen := m.inside[vehicleID]
	if !seen {
		state = make(map[string]membership)
		m.inside[vehicleID] = state
	}

	var transitions []Transition
	for _, z := range m.zones {
		now := z.Contains(p)
		prev, was := state[z.Name()]
		switch {
		case now && !was:
			state[z.Name()] = membership{since: at}
			if seen {
				transitions = append(transitions, Transition{Zone: z.Name(), VehicleID: vehicleID,
					Event: Enter, Point: p, At: at})
			}
		case !now && was:
			delete(state, z.Name())
			transitions = append(transitions, Transition{Zone: z.Name(), VehicleID: vehicleID,
				Event: Exit, Point: p, At: at, Dwell: at.Sub(prev.since)})
		}
	}
	return transitions
}

// Inside lists the zones a vehicle is currently within, sorted by name.
func (m *Monitor) Inside(vehicleID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var names []string
	for z := range m.inside[vehicleID] {
		names = append(names, z)
	}
	sort.Strings(names)
	return names
}

// Occupancy returns, for every zone, the vehicles currently inside it.
func (m *Monitor) Occupancy() map[string][]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string][]string, len(m.zones))
	for _, z := range m.zones {
		out[z.Name()] = []string{}
	}
	for v, zones := range m.inside {
		for z := range zones {
			out[z] = append(out[z], v)
		}
	}
	for _, vs := range out {
		sort.Strings(vs)
	}
	return out
}

// Forget drops all state for a vehicle (e.g. when it is retired from the fleet).
func (m *Monitor) Forget(vehicleID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.inside, vehicleID)
}
