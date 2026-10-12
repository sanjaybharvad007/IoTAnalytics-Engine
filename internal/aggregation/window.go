// Package aggregation turns raw telemetry into rolling operational metrics.
//
// The core primitive is a per-vehicle sliding time window: it retains only the
// readings inside the configured span, evicting aged-out samples as new ones
// arrive, so every metric reflects the vehicle's *current* behaviour rather
// than its whole history. Fleet manages one window per vehicle.
package aggregation

import (
	"sort"
	"sync"
	"time"

	"iotanalytics/internal/geo"
	"iotanalytics/internal/telematics"
)

// Snapshot is the set of rolling metrics computed over a window in one pass.
type Snapshot struct {
	VehicleID   string    `json:"vehicle_id"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Samples     int       `json:"samples"`
	AvgSpeed    float64   `json:"avg_speed"`     // km/h
	MaxSpeed    float64   `json:"max_speed"`     // km/h
	IdleCount   int       `json:"idle_count"`    // samples spent idling
	IdleRatio   float64   `json:"idle_ratio"`    // idle samples / engine-on samples
	DistanceM   float64   `json:"distance_m"`    // great-circle distance travelled
	FuelUsedPct float64   `json:"fuel_used_pct"` // fuel drop across the window
	FuelLevel   float64   `json:"fuel_level"`    // latest fuel level
	EngineOn    bool      `json:"engine_on"`
	Lat         float64   `json:"lat"`
	Lng         float64   `json:"lng"`
	Heading     float64   `json:"heading"`
}

// Window holds the recent readings of a single vehicle.
// It is safe for concurrent use.
type Window struct {
	mu       sync.RWMutex
	span     time.Duration
	readings []telematics.Reading // sorted by Timestamp ascending
}

// NewWindow creates a window retaining readings within span of the newest one.
func NewWindow(span time.Duration) *Window {
	if span <= 0 {
		span = time.Minute
	}
	return &Window{span: span}
}

// Span returns the window's retention period.
func (w *Window) Span() time.Duration { return w.span }

// Add inserts a reading (keeping chronological order even if samples arrive
// slightly late) and evicts everything older than the newest reading minus the
// span. It returns false if the reading is too old to fall inside the window.
func (w *Window) Add(r telematics.Reading) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := len(w.readings)
	if n > 0 && r.Timestamp.Before(w.readings[n-1].Timestamp.Add(-w.span)) {
		return false // older than the window: nothing to do
	}

	// Fast path: in-order arrival.
	if n == 0 || !r.Timestamp.Before(w.readings[n-1].Timestamp) {
		w.readings = append(w.readings, r)
	} else {
		i := sort.Search(n, func(i int) bool { return w.readings[i].Timestamp.After(r.Timestamp) })
		w.readings = append(w.readings, telematics.Reading{})
		copy(w.readings[i+1:], w.readings[i:])
		w.readings[i] = r
	}

	cutoff := w.readings[len(w.readings)-1].Timestamp.Add(-w.span)
	i := 0
	for i < len(w.readings) && w.readings[i].Timestamp.Before(cutoff) {
		i++
	}
	if i > 0 {
		// Re-slice from the front; copy occasionally so memory of evicted
		// readings can be reclaimed instead of pinning the backing array.
		w.readings = w.readings[i:]
		if cap(w.readings) > 4*len(w.readings) && len(w.readings) > 0 {
			fresh := make([]telematics.Reading, len(w.readings))
			copy(fresh, w.readings)
			w.readings = fresh
		}
	}
	return true
}

// Size returns the number of readings currently retained.
func (w *Window) Size() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.readings)
}

// Latest returns the newest reading and whether one exists.
func (w *Window) Latest() (telematics.Reading, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.readings) == 0 {
		return telematics.Reading{}, false
	}
	return w.readings[len(w.readings)-1], true
}

// AvgSpeed is a convenience accessor for Snapshot().AvgSpeed.
func (w *Window) AvgSpeed() float64 { return w.Snapshot().AvgSpeed }

// IdleCount is a convenience accessor for Snapshot().IdleCount.
func (w *Window) IdleCount() int { return w.Snapshot().IdleCount }

// Snapshot computes every rolling metric in a single pass over the window.
func (w *Window) Snapshot() Snapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var s Snapshot
	n := len(w.readings)
	if n == 0 {
		return s
	}
	first, last := w.readings[0], w.readings[n-1]
	s.VehicleID = last.VehicleID
	s.WindowStart, s.WindowEnd = first.Timestamp, last.Timestamp
	s.Samples = n
	s.FuelLevel, s.EngineOn = last.FuelLevel, last.EngineOn
	s.Lat, s.Lng, s.Heading = last.Lat, last.Lng, last.Heading

	var speedSum float64
	engineOn := 0
	var prevFix geo.Point
	havePrev := false
	for _, r := range w.readings {
		speedSum += r.Speed
		if r.Speed > s.MaxSpeed {
			s.MaxSpeed = r.Speed
		}
		if r.EngineOn {
			engineOn++
			if r.IsIdling() {
				s.IdleCount++
			}
		}
		if r.HasFix() {
			p := r.Position()
			if havePrev {
				s.DistanceM += geo.Distance(prevFix, p)
			}
			prevFix, havePrev = p, true
		}
	}
	s.AvgSpeed = speedSum / float64(n)
	if engineOn > 0 {
		s.IdleRatio = float64(s.IdleCount) / float64(engineOn)
	}
	if d := first.FuelLevel - last.FuelLevel; d > 0 {
		s.FuelUsedPct = d
	}
	return s
}
