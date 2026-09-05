// Package aggregation computes rolling operational metrics over a sliding time
// window — the "analytics" that turn raw telematics into insights (avg speed,
// idle time, distance). A sliding window is the standard real-time analytics
// primitive: it keeps only recent data and drops what's aged out.
package aggregation

import (
	"time"

	"iotanalytics/telematics-go"
)

// Window holds recent readings for one vehicle within a time span.
type Window struct {
	span     time.Duration
	readings []telematics.Reading
}

func NewWindow(span time.Duration) *Window {
	return &Window{span: span}
}

// Add appends a reading and evicts any older than the window span.
func (w *Window) Add(r telematics.Reading) {
	w.readings = append(w.readings, r)
	cutoff := r.Timestamp.Add(-w.span)
	i := 0
	for i < len(w.readings) && w.readings[i].Timestamp.Before(cutoff) {
		i++
	}
	w.readings = w.readings[i:]
}

// AvgSpeed over the current window.
func (w *Window) AvgSpeed() float64 {
	if len(w.readings) == 0 {
		return 0
	}
	var sum float64
	for _, r := range w.readings {
		sum += r.Speed
	}
	return sum / float64(len(w.readings))
}

// IdleCount = readings where the engine is on but the vehicle isn't moving
// (a key efficiency metric — idling wastes fuel).
func (w *Window) IdleCount() int {
	n := 0
	for _, r := range w.readings {
		if r.EngineOn && r.Speed < 1.0 {
			n++
		}
	}
	return n
}

func (w *Window) Size() int { return len(w.readings) }
