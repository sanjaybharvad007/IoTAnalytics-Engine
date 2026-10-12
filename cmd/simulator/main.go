// Command simulator plays the role of a fleet of telematics devices: it
// generates synthetic readings and POSTs them to a running server over the
// public ingest API, exactly as real hardware or a gateway would.
//
//	go run ./cmd/server &
//	go run ./cmd/simulator -url http://localhost:8080 -vehicles 8
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"iotanalytics/internal/simulator"
	"iotanalytics/internal/telematics"
)

func main() {
	url := flag.String("url", "http://localhost:8080", "base URL of the analytics server")
	vehicles := flag.Int("vehicles", 6, "number of simulated vehicles")
	interval := flag.Duration("interval", time.Second, "sample interval")
	seed := flag.Int64("seed", time.Now().UnixNano(), "random seed (default: time-based)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 5 * time.Second}
	endpoint := *url + "/api/v1/telemetry"
	fleet := simulator.NewFleet(simulator.Options{Vehicles: *vehicles, Seed: *seed, Interval: *interval})
	log.Info("simulator started", "endpoint", endpoint, "vehicles", fleet.Vehicles())

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	var sent, accepted, alerts int
	for {
		select {
		case <-ctx.Done():
			log.Info("stopped", "sent", sent, "accepted", accepted, "alerts", alerts)
			return
		case now := <-ticker.C:
			batch := fleet.Step(now.UTC())
			sent += len(batch)
			resp, err := post(ctx, client, endpoint, batch)
			if err != nil {
				log.Warn("post failed", "err", err)
				continue
			}
			accepted += resp.Accepted
			alerts += len(resp.Alerts)
			for _, a := range resp.Alerts {
				log.Warn("alert", "vehicle", a.VehicleID, "kind", a.Kind, "severity", a.Severity.String(), "msg", a.Message)
			}
			for _, t := range resp.Transitions {
				log.Info("geofence", "vehicle", t.VehicleID, "zone", t.Zone, "event", t.Event)
			}
			if resp.Rejected > 0 {
				log.Warn("readings rejected", "count", resp.Rejected, "first", resp.Errors[0].Error)
			}
		}
	}
}

// ingestResponse mirrors api.IngestResponse without importing the server package.
type ingestResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	Errors   []struct {
		Error string `json:"error"`
	} `json:"errors"`
	Alerts []struct {
		VehicleID string `json:"vehicle_id"`
		Kind      string `json:"kind"`
		Severity  severity
		Message   string `json:"message"`
	} `json:"alerts"`
	Transitions []struct {
		VehicleID string `json:"vehicle_id"`
		Zone      string `json:"zone"`
		Event     string `json:"event"`
	} `json:"transitions"`
}

type severity string

func (s severity) String() string { return string(s) }

func post(ctx context.Context, client *http.Client, endpoint string, batch []telematics.Reading) (*ingestResponse, error) {
	body, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(data))
	}
	var out ingestResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}
