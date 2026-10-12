package main

import (
	"net/http"
	"net/http/httptest"
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

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	if code := probe(addr); code != 0 {
		t.Fatalf("healthy server probed as %d", code)
	}
	if code := probe("127.0.0.1:1"); code != 1 {
		t.Fatalf("unreachable server probed as %d", code)
	}
}
