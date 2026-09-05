// Package geofence implements geofencing alerts — a core fleet/asset feature:
// fire an alert when a vehicle enters or leaves a defined geographic zone.
// Uses the haversine formula for real great-circle distance on the Earth's
// surface, so circular geofences are accurate. Pure logic, fully testable.
package geofence

import "math"

// Point is a GPS coordinate.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Zone is a circular geofence: a center and a radius in meters.
type Zone struct {
	Name     string  `json:"name"`
	Center   Point   `json:"center"`
	RadiusM  float64 `json:"radius_m"`
}

const earthRadiusM = 6371000.0

// haversine returns the great-circle distance (meters) between two points.
func haversine(a, b Point) float64 {
	lat1 := a.Lat * math.Pi / 180
	lat2 := b.Lat * math.Pi / 180
	dLat := (b.Lat - a.Lat) * math.Pi / 180
	dLng := (b.Lng - a.Lng) * math.Pi / 180

	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthRadiusM * 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}

// Contains reports whether a point is inside the zone.
func (z Zone) Contains(p Point) bool {
	return haversine(z.Center, p) <= z.RadiusM
}

// Transition describes a vehicle crossing a geofence boundary.
type Transition struct {
	Zone     string
	Vehicle  string
	Event    string // "enter" or "exit"
}

// Monitor tracks each vehicle's inside/outside state per zone so it can emit an
// alert only on the TRANSITION (enter/exit), not on every reading.
type Monitor struct {
	zones  []Zone
	inside map[string]map[string]bool // vehicle -> zone -> inside?
}

func NewMonitor(zones ...Zone) *Monitor {
	return &Monitor{zones: zones, inside: make(map[string]map[string]bool)}
}

// Update feeds a vehicle's new position and returns any enter/exit transitions.
func (m *Monitor) Update(vehicle string, p Point) []Transition {
	if m.inside[vehicle] == nil {
		m.inside[vehicle] = make(map[string]bool)
	}
	var transitions []Transition
	for _, z := range m.zones {
		now := z.Contains(p)
		was := m.inside[vehicle][z.Name]
		if now && !was {
			transitions = append(transitions, Transition{z.Name, vehicle, "enter"})
		} else if !now && was {
			transitions = append(transitions, Transition{z.Name, vehicle, "exit"})
		}
		m.inside[vehicle][z.Name] = now
	}
	return transitions
}
