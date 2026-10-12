package main

import (
	"testing"
	"time"

	"iotanalytics/internal/geofence"
)

func TestParseConfigDefaultsAndEnv(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":8080" || cfg.window != 5*time.Minute || cfg.simulate != 0 || cfg.syncMode != "always" {
		t.Fatalf("defaults = %+v", cfg)
	}

	t.Setenv("IOT_ADDR", ":9090")
	t.Setenv("IOT_SIMULATE", "4")
	t.Setenv("IOT_WINDOW", "90s")
	t.Setenv("IOT_PERSIST_READINGS", "true")
	cfg, err = parseConfig([]string{"-speed-limit", "80"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":9090" || cfg.simulate != 4 || cfg.window != 90*time.Second || !cfg.persistReadings || cfg.speedLimit != 80 {
		t.Fatalf("env/flags = %+v", cfg)
	}

	if _, err := parseConfig([]string{"-sync", "sometimes"}); err == nil {
		t.Fatal("invalid sync mode should be rejected")
	}
}

func TestDefaultZonesAreValid(t *testing.T) {
	zones := defaultZones()
	if len(zones) != 3 {
		t.Fatalf("zones = %d", len(zones))
	}
	for _, z := range zones {
		if _, err := geofence.FromSpec(z.Spec()); err != nil {
			t.Errorf("zone %s: %v", z.Name(), err)
		}
	}
}
