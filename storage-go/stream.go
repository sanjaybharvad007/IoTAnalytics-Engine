// Live-update broker (SSE-style fan-out) so a dashboard/mobile client can
// receive alerts and geofence events in real time. Non-blocking broadcast.
package storage

import "sync"

type Broker struct {
	mu   sync.RWMutex
	subs map[chan string]struct{}
}

func NewBroker() *Broker { return &Broker{subs: make(map[chan string]struct{})} }

func (b *Broker) Subscribe() chan string {
	ch := make(chan string, 16)
	b.mu.Lock(); b.subs[ch] = struct{}{}; b.mu.Unlock()
	return ch
}

func (b *Broker) Publish(msg string) {
	b.mu.RLock(); defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *Broker) Count() int { b.mu.RLock(); defer b.mu.RUnlock(); return len(b.subs) }
