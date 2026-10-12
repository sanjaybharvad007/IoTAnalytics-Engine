// Package telematics models the raw operational events an IoT field-operations
// platform ingests: periodic samples from vehicles and equipment.
package telematics

import (
	"errors"
	"fmt"
	"time"

	"iotanalytics/internal/geo"
)

// Reading is a single telemetry sample from a vehicle or asset.
type Reading struct {
	VehicleID string    `json:"vehicle_id"`
	Timestamp time.Time `json:"timestamp"`
	Speed     float64   `json:"speed"`      // km/h
	FuelLevel float64   `json:"fuel_level"` // percent, 0-100
	EngineOn  bool      `json:"engine_on"`
	Lat       float64   `json:"lat"`
	Lng       float64   `json:"lng"`
	Heading   float64   `json:"heading,omitempty"` // degrees, 0-360
}

// Position returns the GPS fix carried by the reading.
func (r Reading) Position() geo.Point { return geo.Point{Lat: r.Lat, Lng: r.Lng} }

// HasFix reports whether the reading carries a usable GPS position.
func (r Reading) HasFix() bool { return !r.Position().IsZero() }

// Validation errors returned by Validate. Callers can match them with errors.Is.
var (
	ErrMissingVehicle = errors.New("telematics: vehicle_id is required")
	ErrMissingTime    = errors.New("telematics: timestamp is required")
	ErrBadSpeed       = errors.New("telematics: speed must be between 0 and 400 km/h")
	ErrBadFuel        = errors.New("telematics: fuel_level must be between 0 and 100")
	ErrBadPosition    = errors.New("telematics: invalid position")
)

// Validate checks the reading for physically plausible values so that bad
// sensors or malformed clients cannot poison the analytics.
func (r Reading) Validate() error {
	switch {
	case r.VehicleID == "":
		return ErrMissingVehicle
	case r.Timestamp.IsZero():
		return ErrMissingTime
	case r.Speed < 0 || r.Speed > 400:
		return fmt.Errorf("%w: got %.1f", ErrBadSpeed, r.Speed)
	case r.FuelLevel < 0 || r.FuelLevel > 100:
		return fmt.Errorf("%w: got %.1f", ErrBadFuel, r.FuelLevel)
	}
	if err := r.Position().Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrBadPosition, err)
	}
	return nil
}

// IsIdling reports whether the engine is running while the vehicle is
// effectively stationary. Idling burns fuel for no work and is a key
// efficiency KPI in fleet operations.
func (r Reading) IsIdling() bool { return r.EngineOn && r.Speed < IdleSpeedKph }

// IdleSpeedKph is the speed below which a running vehicle counts as idling.
// GPS jitter on a parked vehicle commonly reports 0-2 km/h.
const IdleSpeedKph = 2.0
