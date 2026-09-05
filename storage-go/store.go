// Package storage persists operational events (readings + alerts) to disk via
// an append-only log — durable across restarts, dependency-free, and replayed
// on startup. Same pattern real write-heavy event stores use (sequential
// appends, replay to rebuild state).
package storage

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// Record is a generic operational event stored to the log.
type Record struct {
	Type    string  `json:"type"`     // "reading" | "alert" | "geofence"
	Vehicle string  `json:"vehicle"`
	Payload string  `json:"payload"`  // JSON or text detail
}

type Store struct {
	mu      sync.RWMutex
	file    *os.File
	records []Record
}

func Open(path string) (*Store, error) {
	s := &Store{}
	// Replay existing records.
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				s.records = append(s.records, r)
			}
		}
		f.Close()
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	s.file = file
	return s, nil
}

// Append durably writes a record (flushed to disk) and keeps it in memory.
func (s *Store) Append(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := s.file.Sync(); err != nil {
		return err
	}
	s.records = append(s.records, r)
	return nil
}

// ByVehicle returns all records for a vehicle.
func (s *Store) ByVehicle(vehicle string) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Record
	for _, r := range s.records {
		if r.Vehicle == vehicle {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

func (s *Store) Close() error { return s.file.Close() }
