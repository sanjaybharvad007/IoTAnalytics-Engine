// Package telematics models real-time vehicle/equipment telemetry — the raw
// operational events an IoT field-ops platform processes.
package telematics

import "time"

// Reading is a single telematics sample from a vehicle or piece of equipment.
type Reading struct {
	VehicleID string    `json:"vehicle_id"`
	Speed     float64   `json:"speed"`      // km/h
	FuelLevel float64   `json:"fuel_level"` // percent 0-100
	EngineOn  bool      `json:"engine_on"`
	Timestamp time.Time `json:"timestamp"`
}
