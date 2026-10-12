package bus

import (
	"sync"
	"testing"
	"time"
)

func TestFanOut(t *testing.T) {
	b := New[string]()
	a, c := b.Subscribe(4), b.Subscribe(4)
	defer a.Close()
	defer c.Close()
	if b.Subscribers() != 2 {
		t.Fatalf("subscribers=%d", b.Subscribers())
	}
	if n := b.Publish("van-1 exited depot"); n != 2 {
		t.Fatalf("delivered=%d", n)
	}
	for _, s := range []*Subscription[string]{a, c} {
		select {
		case got := <-s.C:
			if got != "van-1 exited depot" {
				t.Fatalf("got %q", got)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive the message")
		}
	}
}

func TestSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	b := New[int]()
	s := b.Subscribe(2)
	defer s.Close()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			b.Publish(i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	if b.Dropped() != 98 || b.Published() != 100 {
		t.Fatalf("dropped=%d published=%d", b.Dropped(), b.Published())
	}
}

func TestCloseUnsubscribesAndIsIdempotent(t *testing.T) {
	b := New[int]()
	s := b.Subscribe(1)
	s.Close()
	s.Close() // must not panic
	if b.Subscribers() != 0 {
		t.Fatal("closed subscription still registered")
	}
	if _, ok := <-s.C; ok {
		t.Fatal("channel should be closed")
	}
	if n := b.Publish(1); n != 0 {
		t.Fatalf("publish to closed subscription delivered %d", n)
	}
}

func TestConcurrentSubscribeCloseAndPublish(t *testing.T) {
	b := New[int]()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				s := b.Subscribe(1)
				s.Close()
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				b.Publish(i)
			}
		}()
	}
	wg.Wait()
	if b.Subscribers() != 0 {
		t.Fatalf("leaked subscriptions: %d", b.Subscribers())
	}
}

func BenchmarkPublish(b *testing.B) {
	bus := New[int]()
	subs := make([]*Subscription[int], 10)
	for i := range subs {
		subs[i] = bus.Subscribe(1 << 16)
	}
	defer func() {
		for _, s := range subs {
			s.Close()
		}
	}()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(i)
	}
}
