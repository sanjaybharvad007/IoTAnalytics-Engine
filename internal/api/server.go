// Package api exposes the engine over HTTP: a JSON REST API for ingest and
// queries, and a Server-Sent Events stream for live updates with resumable
// delivery (Last-Event-ID). It relies only on net/http and the Go 1.22
// pattern router.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"iotanalytics/internal/engine"
)

// Option configures a Server.
type Option func(*Server)

// WithLogger sets the request logger (default slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(s *Server) { s.log = l } }

// WithCORS allows cross-origin browser clients from origin ("*" for any).
// Empty disables CORS headers.
func WithCORS(origin string) Option { return func(s *Server) { s.corsOrigin = origin } }

// WithMaxBody caps the request body size for ingest (default 1 MiB).
func WithMaxBody(n int64) Option { return func(s *Server) { s.maxBody = n } }

// WithHeartbeat sets the SSE keep-alive interval (default 15s).
func WithHeartbeat(d time.Duration) Option { return func(s *Server) { s.heartbeat = d } }

// WithExtraHandler mounts an additional handler at pattern (e.g. "GET /metrics").
func WithExtraHandler(pattern string, h http.Handler) Option {
	return func(s *Server) { s.extra = append(s.extra, mount{pattern, h}) }
}

type mount struct {
	pattern string
	handler http.Handler
}

// Server is the HTTP front-end for an engine.
type Server struct {
	eng        *engine.Engine
	log        *slog.Logger
	corsOrigin string
	maxBody    int64
	heartbeat  time.Duration
	extra      []mount
	handler    http.Handler
}

// New builds the HTTP handler tree for eng.
func New(eng *engine.Engine, opts ...Option) *Server {
	s := &Server{eng: eng, log: slog.Default(), corsOrigin: "*", maxBody: 1 << 20, heartbeat: 15 * time.Second}
	for _, o := range opts {
		o(s)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleHealth)
	mux.HandleFunc("POST /api/v1/telemetry", s.handleIngest)
	mux.HandleFunc("GET /api/v1/fleet", s.handleFleet)
	mux.HandleFunc("GET /api/v1/vehicles", s.handleVehicles)
	mux.HandleFunc("GET /api/v1/vehicles/{id}", s.handleVehicle)
	mux.HandleFunc("GET /api/v1/alerts", s.handleAlerts)
	mux.HandleFunc("GET /api/v1/zones", s.handleZones)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	mux.HandleFunc("GET /api/v1/stats", s.handleStats)
	mux.HandleFunc("GET /api/v1/stream", s.handleStream)
	for _, m := range s.extra {
		mux.Handle(m.pattern, m.handler)
	}
	s.handler = s.recoverer(s.requestID(s.cors(s.logging(mux))))
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// ---- middleware ----------------------------------------------------------

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

// WriteHeader records the status code before delegating.
func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Write counts bytes and defaults the status to 200 on first write.
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush lets SSE handlers reach the underlying flusher through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		f.Flush()
	}
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "dur_ms", float64(time.Since(start).Microseconds())/1000,
			"req_id", w.Header().Get("X-Request-ID"))
	})
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.corsOrigin != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", s.corsOrigin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID, X-Request-ID")
			h.Set("Access-Control-Expose-Headers", "X-Request-ID")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec) // let net/http handle client aborts as designed
				}
				s.log.Error("panic", "err", rec, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func queryInt(r *http.Request, key string, def, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

func readBody(r *http.Request, max int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, max))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, fmt.Errorf("body exceeds %d bytes", max)
		}
		return nil, err
	}
	return body, nil
}
