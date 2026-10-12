// Package bus is a small, generic in-process publish/subscribe fan-out. The
// engine publishes every operational event on a Bus; HTTP streaming handlers,
// metrics and tests subscribe. Publish never blocks: a subscriber that falls
// behind has messages dropped (and counted) rather than stalling the pipeline,
// which is the right trade-off for live telemetry where freshness beats
// completeness and durable replay is available from the store.
package bus

import (
	"sync"
	"sync/atomic"
)

// Bus fans values of type T out to every active subscription.
// The zero value is not usable; call New.
type Bus[T any] struct {
	mu      sync.RWMutex
	subs    map[*Subscription[T]]struct{}
	dropped atomic.Uint64
	sent    atomic.Uint64
}

// New creates an empty bus.
func New[T any]() *Bus[T] { return &Bus[T]{subs: make(map[*Subscription[T]]struct{})} }

// Subscription receives values on C until Close is called.
type Subscription[T any] struct {
	C    <-chan T
	ch   chan T
	bus  *Bus[T]
	once sync.Once
}

// Subscribe registers a subscriber with the given channel buffer. A larger
// buffer tolerates longer consumer pauses before messages are dropped.
func (b *Bus[T]) Subscribe(buffer int) *Subscription[T] {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan T, buffer)
	s := &Subscription[T]{C: ch, ch: ch, bus: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Close unregisters the subscription and closes its channel. It is safe to
// call more than once and safe to call concurrently with Publish.
func (s *Subscription[T]) Close() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		s.bus.mu.Unlock()
		close(s.ch)
	})
}

// Publish delivers v to every subscriber without blocking and returns how
// many received it. Slow subscribers miss the message.
func (b *Bus[T]) Publish(v T) (delivered int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- v:
			delivered++
		default:
			b.dropped.Add(1)
		}
	}
	b.sent.Add(1)
	return delivered
}

// Subscribers returns the number of active subscriptions.
func (b *Bus[T]) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Dropped returns how many deliveries were skipped because a subscriber's
// buffer was full.
func (b *Bus[T]) Dropped() uint64 { return b.dropped.Load() }

// Published returns the total number of Publish calls.
func (b *Bus[T]) Published() uint64 { return b.sent.Load() }
