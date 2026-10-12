package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"iotanalytics/internal/aggregation"
	"iotanalytics/internal/alerts"
	"iotanalytics/internal/engine"
	"iotanalytics/internal/geofence"
	"iotanalytics/internal/storage"
	"iotanalytics/internal/telematics"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "last_seq": s.eng.Store().LastSeq()})
}

// IngestResponse is returned by POST /api/v1/telemetry.
type IngestResponse struct {
	Accepted    int                   `json:"accepted"`
	Rejected    int                   `json:"rejected"`
	Errors      []IngestError         `json:"errors,omitempty"`
	Alerts      []alerts.Alert        `json:"alerts"`
	Transitions []geofence.Transition `json:"transitions"`
}

// IngestError describes one rejected reading in a batch.
type IngestError struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

// handleIngest accepts a single reading or a JSON array of readings.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, s.maxBody)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	body = bytes.TrimSpace(body)
	var readings []telematics.Reading
	switch {
	case len(body) == 0:
		writeError(w, http.StatusBadRequest, "empty body")
		return
	case body[0] == '[':
		if err := json.Unmarshal(body, &readings); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON array: "+err.Error())
			return
		}
	default:
		var one telematics.Reading
		if err := json.Unmarshal(body, &one); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		readings = []telematics.Reading{one}
	}

	resp := IngestResponse{Alerts: []alerts.Alert{}, Transitions: []geofence.Transition{}}
	for i, rd := range readings {
		res, err := s.eng.Ingest(rd)
		if err != nil && !errors.Is(err, engine.ErrPersist) {
			// Validation failure: nothing was ingested. (A persistence error
			// still counts as accepted: the reading was analysed and streamed.)
			resp.Rejected++
			resp.Errors = append(resp.Errors, IngestError{Index: i, Error: err.Error()})
			continue
		}
		resp.Accepted++
		resp.Alerts = append(resp.Alerts, res.Alerts...)
		resp.Transitions = append(resp.Transitions, res.Transitions...)
	}
	status := http.StatusAccepted
	if resp.Accepted == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, resp)
}

// FleetResponse is returned by GET /api/v1/fleet.
type FleetResponse struct {
	Summary  aggregation.Summary    `json:"summary"`
	Vehicles []aggregation.Snapshot `json:"vehicles"`
}

func (s *Server) handleFleet(w http.ResponseWriter, _ *http.Request) {
	fleet := s.eng.Fleet()
	writeJSON(w, http.StatusOK, FleetResponse{Summary: fleet.Summarize(), Vehicles: fleet.Snapshots()})
}

func (s *Server) handleVehicles(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Fleet().Snapshots())
}

// VehicleResponse is returned by GET /api/v1/vehicles/{id}.
type VehicleResponse struct {
	Snapshot aggregation.Snapshot `json:"snapshot"`
	Zones    []string             `json:"zones"`
	Events   []storage.Event      `json:"events"`
}

func (s *Server) handleVehicle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	snap, ok := s.eng.Fleet().Snapshot(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown vehicle "+strconv.Quote(id))
		return
	}
	zones := s.eng.Geofences().Inside(id)
	if zones == nil {
		zones = []string{}
	}
	events := s.eng.Store().ByVehicle(id, queryInt(r, "limit", 50, 1000))
	if events == nil {
		events = []storage.Event{}
	}
	writeJSON(w, http.StatusOK, VehicleResponse{Snapshot: snap, Zones: zones, Events: events})
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	recent := s.eng.Alerts().Recent(queryInt(r, "limit", 50, 1000))
	if sev := r.URL.Query().Get("severity"); sev != "" {
		min, err := alerts.ParseSeverity(sev)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		filtered := recent[:0]
		for _, a := range recent {
			if a.Severity >= min {
				filtered = append(filtered, a)
			}
		}
		recent = filtered
	}
	if recent == nil {
		recent = []alerts.Alert{}
	}
	writeJSON(w, http.StatusOK, recent)
}

// ZonesResponse is returned by GET /api/v1/zones.
type ZonesResponse struct {
	Zones     []geofence.Spec     `json:"zones"`
	Occupancy map[string][]string `json:"occupancy"`
}

func (s *Server) handleZones(w http.ResponseWriter, _ *http.Request) {
	m := s.eng.Geofences()
	writeJSON(w, http.StatusOK, ZonesResponse{Zones: m.Zones(), Occupancy: m.Occupancy()})
}

// EventsResponse is returned by GET /api/v1/events.
type EventsResponse struct {
	Events    []storage.Event `json:"events"`
	Truncated bool            `json:"truncated"`
	LastSeq   uint64          `json:"last_seq"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	st := s.eng.Store()
	limit := queryInt(r, "limit", 100, 1000)
	var (
		events    []storage.Event
		truncated bool
	)
	if after := r.URL.Query().Get("after"); after != "" {
		seq, err := strconv.ParseUint(after, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "after must be a sequence number")
			return
		}
		events, truncated = st.Since(seq, limit)
	} else {
		events = st.Recent(limit, storage.EventType(r.URL.Query().Get("type")))
	}
	if events == nil {
		events = []storage.Event{}
	}
	writeJSON(w, http.StatusOK, EventsResponse{Events: events, Truncated: truncated, LastSeq: st.LastSeq()})
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Stats())
}
