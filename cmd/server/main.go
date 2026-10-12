// Command server runs the IoT analytics engine as an HTTP service: it ingests
// telemetry over REST, computes rolling fleet metrics, raises safety alerts,
// tracks geofences, persists events durably, streams them live over SSE,
// serves an embedded operations dashboard at / and Prometheus metrics at
// /metrics.
//
//	go run ./cmd/server -simulate 6     # built-in fleet simulator
//	go run ./cmd/server                  # ingest from real devices via POST /api/v1/telemetry
//
// Every flag can also be set through an IOT_* environment variable
// (e.g. IOT_ADDR=:9090), which is how the container image is configured.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"iotanalytics/internal/api"
	"iotanalytics/internal/engine"
	"iotanalytics/internal/geo"
	"iotanalytics/internal/geofence"
	"iotanalytics/internal/metrics"
	"iotanalytics/internal/simulator"
	"iotanalytics/internal/storage"
	"iotanalytics/internal/telematics"
	"iotanalytics/web"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	addr            string
	dataPath        string
	zonesPath       string
	window          time.Duration
	cooldown        time.Duration
	speedLimit      float64
	persistReadings bool
	syncMode        string
	simulate        int
	tick            time.Duration
	seed            int64
	corsOrigin      string
	logFormat       string
	logLevel        string
	healthcheck     bool
}

func parseConfig(args []string) (config, error) {
	var c config
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.StringVar(&c.addr, "addr", envStr("IOT_ADDR", ":8080"), "HTTP listen address")
	fs.StringVar(&c.dataPath, "data", envStr("IOT_DATA", "data/ops.log"), "path of the durable event log")
	fs.StringVar(&c.zonesPath, "zones", envStr("IOT_ZONES", ""), "JSON file of geofence zones (default: built-in San Jose demo zones)")
	fs.DurationVar(&c.window, "window", envDur("IOT_WINDOW", 5*time.Minute), "sliding window span per vehicle")
	fs.DurationVar(&c.cooldown, "cooldown", envDur("IOT_COOLDOWN", time.Minute), "minimum gap between repeat alerts of one kind per vehicle")
	fs.Float64Var(&c.speedLimit, "speed-limit", envFloat("IOT_SPEED_LIMIT", 100), "speeding threshold in km/h")
	fs.BoolVar(&c.persistReadings, "persist-readings", envBool("IOT_PERSIST_READINGS", false), "write every reading to the event log (not just alerts/geofence)")
	fs.StringVar(&c.syncMode, "sync", envStr("IOT_SYNC", "always"), "event log durability: always (fsync per event) or none")
	fs.IntVar(&c.simulate, "simulate", envInt("IOT_SIMULATE", 0), "run an in-process fleet simulator with N vehicles (0 = off)")
	fs.DurationVar(&c.tick, "tick", envDur("IOT_TICK", time.Second), "simulator sample interval")
	fs.Int64Var(&c.seed, "seed", int64(envInt("IOT_SEED", 1)), "simulator random seed")
	fs.StringVar(&c.corsOrigin, "cors", envStr("IOT_CORS", "*"), "Access-Control-Allow-Origin value (empty disables CORS)")
	fs.StringVar(&c.logFormat, "log-format", envStr("IOT_LOG_FORMAT", "text"), "log format: text or json")
	fs.StringVar(&c.logLevel, "log-level", envStr("IOT_LOG_LEVEL", "info"), "log level: debug, info, warn, error")
	fs.BoolVar(&c.healthcheck, "healthcheck", false, "probe a running server's /healthz and exit (for container HEALTHCHECKs)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.syncMode != "always" && c.syncMode != "none" {
		return c, fmt.Errorf("-sync must be 'always' or 'none', got %q", c.syncMode)
	}
	return c, nil
}

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if cfg.healthcheck {
		os.Exit(probe(cfg.addr))
	}
	log := newLogger(cfg)
	if err := run(cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Durable store.
	if dir := filepath.Dir(cfg.dataPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create data dir: %w", err)
		}
	}
	sync := storage.SyncAlways
	if cfg.syncMode == "none" {
		sync = storage.SyncNone
	}
	store, err := storage.Open(cfg.dataPath, storage.WithSync(sync))
	if err != nil {
		return err
	}
	defer store.Close()
	log.Info("starting", "version", version, "go", runtime.Version())
	log.Info("event log opened", "path", cfg.dataPath, "replayed", store.Replayed,
		"discarded", store.Discarded, "last_seq", store.LastSeq(), "sync", cfg.syncMode)

	// Geofences.
	zones := defaultZones()
	if cfg.zonesPath != "" {
		if zones, err = geofence.LoadFile(cfg.zonesPath); err != nil {
			return err
		}
	}

	// Engine.
	eng, err := engine.New(engine.Config{
		WindowSpan:      cfg.window,
		Cooldown:        cfg.cooldown,
		Rules:           engine.DefaultRules(cfg.speedLimit),
		Zones:           zones,
		Store:           store,
		PersistReadings: cfg.persistReadings,
		Logger:          log,
	})
	if err != nil {
		return err
	}
	log.Info("engine ready", "window", cfg.window, "cooldown", cfg.cooldown, "rules", eng.Alerts().Rules(),
		"zones", len(zones), "speed_limit", cfg.speedLimit)

	// Observability + dashboard.
	exporter := metrics.New(eng)
	defer exporter.Close()

	// HTTP.
	handler := api.New(eng,
		api.WithLogger(log),
		api.WithCORS(cfg.corsOrigin),
		api.WithExtraHandler("GET /metrics", exporter),
		api.WithExtraHandler("GET /", web.Handler()),
	)
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		// WriteTimeout is intentionally unset: the SSE stream is long-lived.
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.addr, "dashboard", "http://localhost"+portOf(cfg.addr)+"/",
			"metrics", "/metrics", "stream", "/api/v1/stream")
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Optional in-process simulator.
	if cfg.simulate > 0 {
		fleet := simulator.NewFleet(simulator.Options{Vehicles: cfg.simulate, Seed: cfg.seed, Interval: cfg.tick})
		log.Info("simulator started", "vehicles", fleet.Vehicles(), "interval", cfg.tick)
		go func() {
			_ = simulator.Run(ctx, fleet, func(r telematics.Reading) {
				if _, err := eng.Ingest(r); err != nil && !errors.Is(err, engine.ErrPersist) {
					log.Warn("simulated reading rejected", "err", err)
				}
			})
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	stats := eng.Stats()
	log.Info("stopped", "ingested", stats.Ingested, "alerts", stats.Alerts,
		"transitions", stats.Transitions, "stored_events", stats.StoredEvents)
	return nil
}

// probe performs an HTTP health check against a server on addr and returns a
// process exit code. The distroless container image has no shell or curl, so
// the binary doubles as its own HEALTHCHECK.
func probe(addr string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1" + portOf(addr) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy:", resp.Status)
		return 1
	}
	return 0
}

// portOf returns the ":port" part of a listen address for log hints.
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ""
}

// defaultZones are demo geofences around San Jose, CA: a depot, the airport,
// and a polygonal downtown service area.
func defaultZones() []geofence.Zone {
	return []geofence.Zone{
		geofence.Circle{ZoneName: "depot", Center: geo.Point{Lat: 37.3382, Lng: -121.8863}, RadiusM: 800},
		geofence.Circle{ZoneName: "sjc_airport", Center: geo.Point{Lat: 37.3639, Lng: -121.9289}, RadiusM: 1500},
		geofence.Polygon{ZoneName: "downtown", Vertices: []geo.Point{
			{Lat: 37.3460, Lng: -121.9050}, {Lat: 37.3460, Lng: -121.8750},
			{Lat: 37.3250, Lng: -121.8750}, {Lat: 37.3250, Lng: -121.9050},
		}},
	}
}

func newLogger(cfg config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.logLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.logFormat == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// ---- env helpers: flags default to IOT_* environment variables -------------

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
