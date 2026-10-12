package alerts

import (
	"sort"
	"sync"
	"time"

	"iotanalytics/internal/telematics"
)

// Option configures an Engine.
type Option func(*Engine)

// WithRules adds rules to the engine. Rules are evaluated in order.
func WithRules(rules ...Rule) Option {
	return func(e *Engine) { e.rules = append(e.rules, rules...) }
}

// WithCooldown suppresses repeat alerts of the same kind for the same vehicle
// within d of the last one, unless the new alert is more severe. Without a
// cooldown a speeding vehicle sampled every second would raise 60 alerts a
// minute, which drowns dispatchers in noise.
func WithCooldown(d time.Duration) Option {
	return func(e *Engine) { e.cooldown = d }
}

// WithHistory bounds how many recent alerts the engine keeps in memory
// (default 1000). Durable history belongs in the storage package.
func WithHistory(n int) Option {
	return func(e *Engine) {
		if n > 0 {
			e.maxHistory = n
		}
	}
}

// Engine runs a set of rules over a stream of readings.
// It is safe for concurrent use.
type Engine struct {
	rules      []Rule
	cooldown   time.Duration
	maxHistory int

	mu        sync.Mutex
	lastFired map[string]Alert // Alert.Key() -> last emitted alert
	history   []Alert          // ring buffer of the most recent alerts
	head      int
	total     uint64
}

// NewEngine builds an engine from options. The zero configuration has no rules.
func NewEngine(opts ...Option) *Engine {
	e := &Engine{maxHistory: 1000, lastFired: make(map[string]Alert)}
	for _, o := range opts {
		o(e)
	}
	e.history = make([]Alert, 0, e.maxHistory)
	return e
}

// Rules lists the names of the configured rules.
func (e *Engine) Rules() []string {
	names := make([]string, len(e.rules))
	for i, r := range e.rules {
		names[i] = r.Name()
	}
	return names
}

// Process evaluates every rule against a reading and returns the alerts that
// survived cooldown de-duplication, ordered most severe first.
func (e *Engine) Process(r telematics.Reading) []Alert {
	var fired []Alert
	for _, rule := range e.rules {
		a := rule.Evaluate(r)
		if a == nil {
			continue
		}
		if a.At.IsZero() {
			a.At = r.Timestamp
		}
		if e.admit(*a) {
			fired = append(fired, *a)
		}
	}
	sort.SliceStable(fired, func(i, j int) bool { return fired[i].Severity > fired[j].Severity })
	return fired
}

// admit applies the cooldown and records the alert. It returns false if the
// alert is a suppressed repeat.
func (e *Engine) admit(a Alert) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if prev, ok := e.lastFired[a.Key()]; ok && e.cooldown > 0 {
		within := a.At.Sub(prev.At) < e.cooldown
		if within && a.Severity <= prev.Severity {
			return false
		}
	}
	e.lastFired[a.Key()] = a
	e.total++

	if len(e.history) < e.maxHistory {
		e.history = append(e.history, a)
	} else {
		e.history[e.head] = a
		e.head = (e.head + 1) % e.maxHistory
	}
	return true
}

// Recent returns up to n of the most recent alerts, newest first.
func (e *Engine) Recent(n int) []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	size := len(e.history)
	if n <= 0 || n > size {
		n = size
	}
	out := make([]Alert, 0, n)
	for i := 0; i < n; i++ {
		// Walk backwards from the newest slot. Before the buffer wraps, head
		// is 0 and this resolves to size-1-i; after wrapping, head points at
		// the oldest slot so head-1 is the newest.
		out = append(out, e.history[(e.head-1-i+size*2)%size])
	}
	return out
}

// Total returns the number of alerts admitted since the engine started.
func (e *Engine) Total() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.total
}

// Reset clears cooldown state and history (useful between simulation runs).
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastFired = make(map[string]Alert)
	e.history = e.history[:0]
	e.head, e.total = 0, 0
}
