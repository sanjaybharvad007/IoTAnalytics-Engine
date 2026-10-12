package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"iotanalytics/internal/storage"
)

// handleStream serves a Server-Sent Events feed of engine events.
//
//	GET /api/v1/stream?types=alert,geofence&after=<seq>
//	Last-Event-ID: <seq>
//
// Clients reconnecting with Last-Event-ID (which browsers' EventSource sends
// automatically) receive every durable event they missed from the store
// before live delivery resumes, so a dropped connection never loses alerts.
// If the requested position has aged out of retention a "gap" event is sent
// first so the client can do a full refresh.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	wanted := parseTypes(r.URL.Query().Get("types"))
	after, resuming, err := resumePoint(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Subscribe before replaying so nothing published during the replay is
	// missed; duplicates are filtered by sequence number below.
	sub := s.eng.Subscribe(256)
	defer sub.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")

	var lastSent uint64
	write := func(ev storage.Event) bool {
		if !wanted[ev.Type] {
			return true
		}
		if ev.Seq != 0 {
			if ev.Seq <= lastSent {
				return true // already delivered during replay
			}
			lastSent = ev.Seq
		}
		return writeEvent(w, ev) == nil
	}

	if resuming {
		missed, truncated := s.eng.Store().Since(after, 0)
		if truncated {
			fmt.Fprintf(w, "event: gap\ndata: {\"after\":%d,\"oldest\":%d}\n\n", after, firstSeq(missed))
		}
		for _, ev := range missed {
			if !write(ev) {
				return
			}
		}
	}
	flusher.Flush()

	heartbeat := time.NewTicker(s.heartbeat)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			if !write(ev) {
				return
			}
			// Drain anything else already queued before flushing once.
		drain:
			for {
				select {
				case ev, ok := <-sub.C:
					if !ok || !write(ev) {
						return
					}
				default:
					break drain
				}
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, ev storage.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if ev.Seq != 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", ev.Seq); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
	return err
}

// parseTypes turns "alert,geofence" into a set; empty means every type.
func parseTypes(raw string) map[storage.EventType]bool {
	all := map[storage.EventType]bool{storage.TypeReading: true, storage.TypeAlert: true, storage.TypeGeofence: true}
	if strings.TrimSpace(raw) == "" {
		return all
	}
	out := make(map[storage.EventType]bool)
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out[storage.EventType(t)] = true
		}
	}
	return out
}

// resumePoint reads the resume position from Last-Event-ID or ?after=.
func resumePoint(r *http.Request) (seq uint64, resuming bool, err error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return 0, false, nil
	}
	seq, err = strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("Last-Event-ID / after must be a sequence number")
	}
	return seq, true, nil
}

func firstSeq(events []storage.Event) uint64 {
	if len(events) == 0 {
		return 0
	}
	return events[0].Seq
}
