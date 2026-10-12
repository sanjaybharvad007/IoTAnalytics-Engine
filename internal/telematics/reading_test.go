package telematics

import (
	"errors"
	"testing"
	"time"
)

func valid() Reading {
	return Reading{VehicleID: "van-1", Timestamp: time.Now(), Speed: 50, FuelLevel: 60,
		EngineOn: true, Lat: 37.33, Lng: -121.88}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Reading)
		want   error
	}{
		{"ok", func(*Reading) {}, nil},
		{"no vehicle", func(r *Reading) { r.VehicleID = "" }, ErrMissingVehicle},
		{"no time", func(r *Reading) { r.Timestamp = time.Time{} }, ErrMissingTime},
		{"negative speed", func(r *Reading) { r.Speed = -1 }, ErrBadSpeed},
		{"absurd speed", func(r *Reading) { r.Speed = 900 }, ErrBadSpeed},
		{"fuel > 100", func(r *Reading) { r.FuelLevel = 101 }, ErrBadFuel},
		{"bad lat", func(r *Reading) { r.Lat = 95 }, ErrBadPosition},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := valid()
			c.mutate(&r)
			if err := r.Validate(); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestIsIdling(t *testing.T) {
	r := valid()
	r.Speed = 0.5
	if !r.IsIdling() {
		t.Fatal("engine on + stationary should be idling")
	}
	r.EngineOn = false
	if r.IsIdling() {
		t.Fatal("engine off is parked, not idling")
	}
}

func TestHasFix(t *testing.T) {
	r := valid()
	if !r.HasFix() {
		t.Fatal("expected fix")
	}
	r.Lat, r.Lng = 0, 0
	if r.HasFix() {
		t.Fatal("null island is not a fix")
	}
}
