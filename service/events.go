package service

import (
	"context"
	"sync"
)

type Topic uint32

const (
	TopicStatus Topic = 1 << iota
	TopicUPs
	TopicChannels
	TopicDeliveries
	TopicBiliLogin
	TopicMicrosoftLogin
	TopicSettings
)

type EventBus struct {
	mu          sync.Mutex
	revision    uint64
	subscribers map[*Subscription]struct{}
}

type Subscription struct {
	bus      *EventBus
	mu       sync.Mutex
	topics   Topic
	revision uint64
	wake     chan struct{}
	done     chan struct{}
	closed   bool
}

func NewEventBus() *EventBus {
	return &EventBus{subscribers: make(map[*Subscription]struct{})}
}

func (b *EventBus) Publish(topics Topic) uint64 {
	if b == nil || topics == 0 {
		return 0
	}
	b.mu.Lock()
	b.revision++
	revision := b.revision
	for subscription := range b.subscribers {
		subscription.mark(topics, revision)
	}
	b.mu.Unlock()
	return revision
}

func (b *EventBus) Revision() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.revision
}

func (b *EventBus) Subscribe() *Subscription {
	subscription := &Subscription{bus: b, wake: make(chan struct{}, 1), done: make(chan struct{})}
	b.mu.Lock()
	b.subscribers[subscription] = struct{}{}
	b.mu.Unlock()
	return subscription
}

func (s *Subscription) mark(topics Topic, revision uint64) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.topics |= topics
	s.revision = revision
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscription) Next(ctx context.Context) (Topic, uint64, error) {
	select {
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	case <-s.done:
		return 0, 0, context.Canceled
	case <-s.wake:
	}
	s.mu.Lock()
	topics, revision := s.topics, s.revision
	s.topics = 0
	s.mu.Unlock()
	return topics, revision, nil
}

func (s *Subscription) Close() {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.mu.Lock()
	delete(s.bus.subscribers, s)
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	s.mu.Unlock()
	s.bus.mu.Unlock()
}
