package storage

import "testing"

func TestBrokerBroadcast(t *testing.T) {
	b := NewBroker()
	ch := b.Subscribe()
	if b.Count() != 1 { t.Fatal("expected 1 subscriber") }
	b.Publish("van-1 exited depot")
	if <-ch != "van-1 exited depot" {
		t.Error("subscriber didn't receive the message")
	}
}
