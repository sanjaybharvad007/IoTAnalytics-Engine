package aggregation

import (
	"sort"
	"sync"
	"time"

	"iotanalytics/internal/telematics"
)

// Fleet maintains one sliding window per vehicle and exposes fleet-wide
// roll-ups. It is safe for concurrent use.
type Fleet struct {
	mu      sync.RWMutex
	span    time.Duration
	windows map[string]*Window
}

// NewFleet creates a fleet aggregator whose per-vehicle windows span the given duration.
func NewFleet(span time.Duration) *Fleet {
	return &Fleet{span: span, windows: make(map[string]*Window)}
}

// Add routes a reading to its vehicle's window, creating the window on first sight.
func (f *Fleet) Add(r telematics.Reading) bool {
	f.mu.RLock()
	w := f.windows[r.VehicleID]
	f.mu.RUnlock()
	if w == nil {
		f.mu.Lock()
		if w = f.windows[r.VehicleID]; w == nil {
			w = NewWindow(f.span)
			f.windows[r.VehicleID] = w
		}
		f.mu.Unlock()
	}
	return w.Add(r)
}

// Window returns the window for a vehicle, or nil if the vehicle is unknown.
func (f *Fleet) Window(vehicleID string) *Window {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.windows[vehicleID]
}

// Snapshot returns the rolling metrics for one vehicle.
func (f *Fleet) Snapshot(vehicleID string) (Snapshot, bool) {
	w := f.Window(vehicleID)
	if w == nil {
		return Snapshot{}, false
	}
	return w.Snapshot(), true
}

// Snapshots returns a snapshot for every vehicle, sorted by vehicle ID so the
// output is stable for APIs and tests.
func (f *Fleet) Snapshots() []Snapshot {
	f.mu.RLock()
	out := make([]Snapshot, 0, len(f.windows))
	for _, w := range f.windows {
		out = append(out, w.Snapshot())
	}
	f.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].VehicleID < out[j].VehicleID })
	return out
}

// Size returns the number of tracked vehicles.
func (f *Fleet) Size() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.windows)
}

// Prune drops vehicles whose newest reading is older than ttl relative to now,
// so a long-running engine does not accumulate windows for retired assets.
// It returns the IDs that were removed.
func (f *Fleet) Prune(now time.Time, ttl time.Duration) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var gone []string
	for id, w := range f.windows {
		if last, ok := w.Latest(); !ok || now.Sub(last.Timestamp) > ttl {
			delete(f.windows, id)
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	return gone
}

// Summary is a fleet-wide roll-up of the per-vehicle snapshots.
type Summary struct {
	Vehicles      int     `json:"vehicles"`
	Moving        int     `json:"moving"`
	Idling        int     `json:"idling"`
	EngineOff     int     `json:"engine_off"`
	AvgSpeed      float64 `json:"avg_speed"`        // mean of per-vehicle averages
	TotalDistM    float64 `json:"total_distance_m"` // sum across the fleet
	LowFuelCount  int     `json:"low_fuel_count"`   // vehicles below 15%
	LowFuelCutoff float64 `json:"low_fuel_cutoff"`
}

// Summarize rolls the current snapshots up into fleet-level KPIs.
func (f *Fleet) Summarize() Summary {
	const lowFuel = 15.0
	s := Summary{LowFuelCutoff: lowFuel}
	var speedSum float64
	snaps := f.Snapshots()
	for i := range snaps {
		snap := &snaps[i]
		s.Vehicles++
		speedSum += snap.AvgSpeed
		s.TotalDistM += snap.DistanceM
		switch {
		case !snap.EngineOn:
			s.EngineOff++
		case snap.Samples > 0 && snap.AvgSpeed < telematics.IdleSpeedKph:
			s.Idling++
		default:
			s.Moving++
		}
		if snap.FuelLevel < lowFuel {
			s.LowFuelCount++
		}
	}
	if s.Vehicles > 0 {
		s.AvgSpeed = speedSum / float64(s.Vehicles)
	}
	return s
}
