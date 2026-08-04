package service

import "testing"

func TestEventBusCoalescesTopicsWithoutLosingRevision(t *testing.T) {
	bus := NewEventBus()
	subscription := bus.Subscribe()
	defer subscription.Close()
	bus.Publish(TopicStatus)
	bus.Publish(TopicUPs | TopicDeliveries)
	topics, revision, err := subscription.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := TopicStatus | TopicUPs | TopicDeliveries; topics != want {
		t.Fatalf("topics=%b, want %b", topics, want)
	}
	if revision != 2 {
		t.Fatalf("revision=%d, want 2", revision)
	}
}
