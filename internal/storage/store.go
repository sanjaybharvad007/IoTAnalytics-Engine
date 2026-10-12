// Package storage persists operational events to an append-only, newline
// delimited JSON log. This is the same write pattern used by production
// event stores (sequential appends, fsync, replay on startup to rebuild
// state) kept dependency-free so the engine runs anywhere.
//
// Every event receives a monotonically increasing sequence number that
// survives restarts; streaming clients use it to resume exactly where they
// left off (see Since).
package storage

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// EventType classifies a stored event.
type EventType string

// Event types written by the engine.
const (
	TypeReading  EventType = "reading"
	TypeAlert    EventType = "alert"
	TypeGeofence EventType = "geofence"
)

// Event is a durable operational record.
type Event struct {
	Seq       uint64          `json:"seq"`
	At        time.Time       `json:"at"`
	Type      EventType       `json:"type"`
	VehicleID string          `json:"vehicle_id"`
	Payload   json.RawMessage `json:"payload"`
}

// SyncMode controls when appends are flushed to stable storage.
type SyncMode int

const (
	// SyncAlways fsyncs after every append: no acknowledged event is ever lost.
	SyncAlways SyncMode = iota
	// SyncNone leaves flushing to the OS page cache: much higher throughput,
	// events from the last few seconds may be lost on power failure.
	SyncNone
)

// Option configures a Store.
type Option func(*Store)

// WithSync sets the durability mode (default SyncAlways).
func WithSync(mode SyncMode) Option { return func(s *Store) { s.sync = mode } }

// WithRetention bounds how many recent events are kept in memory for
// Since/ByVehicle queries (default 10,000). The on-disk log is unbounded.
func WithRetention(n int) Option {
	return func(s *Store) {
		if n > 0 {
			s.retain = n
		}
	}
}

// Store is an append-only event log. It is safe for concurrent use.
type Store struct {
	sync   SyncMode
	retain int

	mu      sync.RWMutex
	file    *os.File
	w       *bufio.Writer
	recent  []Event // ring buffer of the newest events
	head    int
	lastSeq uint64
	count   uint64 // total events in the log, including replayed ones
	closed  bool

	// Recovery stats from Open.
	Replayed  int // events read back from disk
	Discarded int // corrupt/partial lines dropped during replay
}

// Open opens (or creates) the log at path, replays it to recover sequence
// state, and truncates any partially written trailing line left by a crash.
func Open(path string, opts ...Option) (*Store, error) {
	s := &Store{sync: SyncAlways, retain: 10_000}
	for _, o := range opts {
		o(s)
	}
	s.recent = make([]Event, 0, min(s.retain, 1024))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	goodBytes, err := s.replay(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	// Drop a torn tail (a crash mid-write) so the log stays well-formed.
	if err := f.Truncate(goodBytes); err != nil {
		f.Close()
		return nil, fmt.Errorf("storage: truncate: %w", err)
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	s.file = f
	s.w = bufio.NewWriterSize(f, 64<<10)
	return s, nil
}

// replay reads the existing log, returning the byte offset just past the last
// well-formed line.
func (s *Store) replay(f *os.File) (int64, error) {
	r := bufio.NewReaderSize(f, 1<<20)
	var offset int64
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			if len(line) > 0 {
				s.Discarded++ // partial last line without newline
			}
			return offset, nil
		}
		if err != nil {
			return 0, fmt.Errorf("storage: replay: %w", err)
		}
		var e Event
		if json.Unmarshal(line, &e) != nil || e.Seq == 0 {
			s.Discarded++
			// Keep scanning: a single bad line should not hide later good ones,
			// but we only trust the prefix up to the last good line for truncation.
			offset += int64(len(line))
			continue
		}
		offset += int64(len(line))
		s.Replayed++
		s.count++
		if e.Seq > s.lastSeq {
			s.lastSeq = e.Seq
		}
		s.remember(e)
	}
}

// remember pushes an event into the in-memory ring buffer (caller holds mu).
func (s *Store) remember(e Event) {
	if len(s.recent) < s.retain {
		s.recent = append(s.recent, e)
		return
	}
	s.recent[s.head] = e
	s.head = (s.head + 1) % s.retain
}

// ErrClosed is returned by Append after Close.
var ErrClosed = errors.New("storage: store is closed")

// Append durably records an event. payload is JSON-encoded; the returned
// Event carries the assigned sequence number and timestamp.
func (s *Store) Append(typ EventType, vehicleID string, payload any) (Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("storage: encode payload: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Event{}, ErrClosed
	}
	e := Event{Seq: s.lastSeq + 1, At: time.Now().UTC(), Type: typ, VehicleID: vehicleID, Payload: raw}
	line, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	if _, err := s.w.Write(append(line, '\n')); err != nil {
		return Event{}, fmt.Errorf("storage: write: %w", err)
	}
	if err := s.w.Flush(); err != nil {
		return Event{}, fmt.Errorf("storage: flush: %w", err)
	}
	if s.sync == SyncAlways {
		if err := s.file.Sync(); err != nil {
			return Event{}, fmt.Errorf("storage: fsync: %w", err)
		}
	}
	s.lastSeq = e.Seq
	s.count++
	s.remember(e)
	return e, nil
}

// snapshotLocked returns retained events oldest-first (caller holds mu).
func (s *Store) snapshotLocked() []Event {
	n := len(s.recent)
	out := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, s.recent[(s.head+i)%n])
	}
	return out
}

// Since returns up to limit retained events with Seq > afterSeq, oldest first.
// truncated is true when events newer than afterSeq have already aged out of
// the in-memory retention, so the caller knows the replay has a gap.
func (s *Store) Since(afterSeq uint64, limit int) (events []Event, truncated bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.snapshotLocked()
	if len(all) > 0 && afterSeq+1 < all[0].Seq {
		truncated = true
	}
	for _, e := range all {
		if e.Seq > afterSeq {
			events = append(events, e)
			if limit > 0 && len(events) >= limit {
				break
			}
		}
	}
	return events, truncated
}

// Recent returns up to n of the newest retained events, newest first,
// optionally filtered by type ("" matches all).
func (s *Store) Recent(n int, typ EventType) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.snapshotLocked()
	out := make([]Event, 0, min(n, len(all)))
	for i := len(all) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		if typ == "" || all[i].Type == typ {
			out = append(out, all[i])
		}
	}
	return out
}

// ByVehicle returns retained events for one vehicle, newest first.
func (s *Store) ByVehicle(vehicleID string, n int) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.snapshotLocked()
	var out []Event
	for i := len(all) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		if all[i].VehicleID == vehicleID {
			out = append(out, all[i])
		}
	}
	return out
}

// Count returns the total number of events in the log (replayed + appended).
func (s *Store) Count() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.count
}

// LastSeq returns the newest sequence number.
func (s *Store) LastSeq() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSeq
}

// Close flushes and closes the log. Further Appends return ErrClosed.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.w.Flush(); err != nil {
		s.file.Close()
		return err
	}
	if err := s.file.Sync(); err != nil {
		s.file.Close()
		return err
	}
	return s.file.Close()
}
