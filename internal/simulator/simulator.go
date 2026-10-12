// Package simulator generates realistic synthetic fleet telemetry so the
// engine can be exercised end-to-end without physical devices. Vehicles
// follow random-walk routes around a depot and a few are assigned behaviour
// profiles (speeder, low fuel, idler, harsh braker) that deterministically
// trigger every alert rule and geofence transition the engine supports.
package simulator

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"iotanalytics/internal/geo"
	"iotanalytics/internal/telematics"
)

// Profile selects a vehicle's scripted behaviour.
type Profile int

// Behaviour profiles, assigned round-robin so a fleet of 5+ covers them all.
const (
	Normal Profile = iota
	Speeder
	LowFuel
	Idler
	HarshBraker
	numProfiles
)

func (p Profile) String() string {
	return [...]string{"normal", "speeder", "low_fuel", "idler", "harsh_braker"}[p]
}

// Options configures a simulated fleet.
type Options struct {
	Vehicles int           // number of vehicles (default 6)
	Center   geo.Point     // depot location (default downtown San Jose)
	RadiusM  float64       // how far vehicles roam from the depot (default 4 km)
	Seed     int64         // RNG seed for reproducible runs (default 1)
	Interval time.Duration // simulated time between samples (default 1s)
}

func (o *Options) defaults() {
	if o.Vehicles <= 0 {
		o.Vehicles = 6
	}
	if o.Center.IsZero() {
		o.Center = geo.Point{Lat: 37.3382, Lng: -121.8863}
	}
	if o.RadiusM <= 0 {
		o.RadiusM = 4000
	}
	if o.Seed == 0 {
		o.Seed = 1
	}
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
}

type vehicle struct {
	id        string
	profile   Profile
	pos       geo.Point
	heading   float64
	speed     float64 // km/h
	target    float64 // cruise speed we accelerate toward
	fuel      float64
	engineOn  bool
	stopUntil float64 // simulated second until which the vehicle stays stopped
	holdUntil float64 // harsh-braker: when to pull away again
	inbound   bool    // heading back to the depot
	tripM     float64 // how far this trip goes before turning back
}

// Fleet is a deterministic telemetry generator.
type Fleet struct {
	opts     Options
	rng      *rand.Rand
	vehicles []*vehicle
	tick     int
	elapsed  float64 // simulated seconds since start
}

// NewFleet creates a fleet. The first vehicle of each profile is placed inside
// the depot so geofence exits happen early in a run.
func NewFleet(opts Options) *Fleet {
	opts.defaults()
	f := &Fleet{opts: opts, rng: rand.New(rand.NewSource(opts.Seed))}
	for i := 0; i < opts.Vehicles; i++ {
		v := &vehicle{
			id:       fmt.Sprintf("van-%02d", i+1),
			profile:  Profile(i % int(numProfiles)),
			pos:      geo.Offset(opts.Center, f.rng.Float64()*360, f.rng.Float64()*300), // start near depot
			heading:  f.rng.Float64() * 360,
			fuel:     60 + f.rng.Float64()*40,
			engineOn: true,
			target:   45 + f.rng.Float64()*25,
		}
		if v.profile == LowFuel {
			v.fuel = 22
		}
		f.vehicles = append(f.vehicles, v)
	}
	return f
}

// Vehicles returns the IDs and profiles of the simulated fleet.
func (f *Fleet) Vehicles() map[string]string {
	out := make(map[string]string, len(f.vehicles))
	for _, v := range f.vehicles {
		out[v.id] = v.profile.String()
	}
	return out
}

// Step advances every vehicle by one interval and returns their readings
// stamped with now.
func (f *Fleet) Step(now time.Time) []telematics.Reading {
	f.tick++
	dt := f.opts.Interval.Seconds()
	f.elapsed += dt
	out := make([]telematics.Reading, 0, len(f.vehicles))
	for _, v := range f.vehicles {
		f.advance(v, dt)
		out = append(out, telematics.Reading{
			VehicleID: v.id, Timestamp: now, Speed: round1(v.speed), FuelLevel: round1(v.fuel),
			EngineOn: v.engineOn, Lat: v.pos.Lat, Lng: v.pos.Lng, Heading: round1(v.heading),
		})
	}
	return out
}

// crossed reports whether a boundary of the given period (in simulated
// seconds) was passed during the current step. All scripted behaviour is
// keyed to simulated time rather than tick counts, so the fleet behaves the
// same whether it is sampled at 10 Hz or every 5 seconds.
func (f *Fleet) crossed(periodSec, dt float64) bool {
	return math.Floor(f.elapsed/periodSec) != math.Floor((f.elapsed-dt)/periodSec)
}

// phase returns where the simulation is inside a repeating cycle, in seconds.
func (f *Fleet) phase(periodSec float64) float64 { return math.Mod(f.elapsed, periodSec) }

func (f *Fleet) advance(v *vehicle, dt float64) {
	r := f.rng
	// Trips: wander outbound until the trip distance is reached, then steer
	// home until back at the depot. This guarantees both geofence exits and
	// re-entries during a run.
	d := geo.Distance(f.opts.Center, v.pos)
	switch {
	case !v.inbound && v.tripM == 0:
		v.tripM = f.opts.RadiusM * (0.4 + 0.6*r.Float64())
	case !v.inbound && d >= v.tripM:
		v.inbound = true
	case v.inbound && d < 150:
		v.inbound, v.tripM = false, 0
	}
	if v.inbound {
		v.heading = math.Mod(geo.Bearing(v.pos, f.opts.Center)+(r.Float64()-0.5)*30+360, 360)
	} else {
		v.heading = math.Mod(v.heading+(r.Float64()-0.5)*20*dt+360, 360)
	}

	accel := 6.0 // km/h per second
	switch v.profile {
	case Speeder:
		// Alternates 40 s of cruising with 20 s bursts well over the limit.
		if f.phase(60) < 40 {
			v.target = 70
		} else {
			v.target = 115 + r.Float64()*25
		}
		accel = 12
	case Idler:
		// Long engine-on stops: 6 minutes stopped, 2 minutes driving.
		if f.phase(480) < 360 {
			v.stopUntil = f.elapsed + dt
		}
	case HarshBraker:
		// Every 90 s slam from cruising speed to near-stop in one sample,
		// then hold for 10 s before pulling away again.
		if f.crossed(90, dt) && v.speed > 60 {
			v.speed, v.target = 5, 5
			v.holdUntil = f.elapsed + 10
		}
		if v.holdUntil > 0 && f.elapsed >= v.holdUntil {
			v.holdUntil, v.target = 0, 65
		}
	default:
		// Occasional short stops (traffic lights, deliveries): on average one
		// every ~100 simulated seconds, lasting 5-45 s.
		if f.elapsed >= v.stopUntil && r.Float64() < 0.01*dt {
			v.stopUntil = f.elapsed + 5 + r.Float64()*40
		}
	}

	if f.elapsed < v.stopUntil {
		v.speed = math.Max(0, v.speed-25*dt) // brake smoothly
	} else {
		// Accelerate/decelerate toward target with a little sensor jitter.
		if v.speed < v.target {
			v.speed = math.Min(v.target, v.speed+accel*dt)
		} else {
			v.speed = math.Max(v.target, v.speed-accel*dt)
		}
		v.speed = math.Max(0, v.speed+(r.Float64()-0.5)*4*dt)
		if v.profile != Speeder && v.profile != HarshBraker && f.crossed(120, dt) {
			v.target = 40 + r.Float64()*30
		}
	}

	// Move along heading: km/h -> m per dt.
	if v.speed > 0 {
		v.pos = geo.Offset(v.pos, v.heading, v.speed/3.6*dt)
	}

	// Fuel burn: base idle burn plus speed-proportional burn (percent per hour).
	burnPerHour := 0.6 + v.speed*0.05
	if v.profile == LowFuel {
		burnPerHour *= 30 // leaky tank: hits the alert thresholds within minutes
	}
	v.fuel = math.Max(0, v.fuel-burnPerHour*dt/3600)
	if v.fuel == 0 {
		v.engineOn, v.speed = false, 0
	}
	if v.fuel < 1 && f.crossed(600, dt) { // refuel occasionally so the demo keeps going
		v.fuel, v.engineOn = 95, true
	}
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// Sink receives generated readings.
type Sink func(telematics.Reading)

// Run drives the fleet in real time, emitting a batch of readings every
// interval to sink until ctx is cancelled. Simulated timestamps use the wall
// clock so the engine's sliding windows behave as they would in production.
func Run(ctx context.Context, f *Fleet, sink Sink) error {
	ticker := time.NewTicker(f.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			for _, r := range f.Step(now.UTC()) {
				sink(r)
			}
		}
	}
}
