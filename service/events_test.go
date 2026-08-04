package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventBusCoalescesTopicsWithoutLosingRevision(t *testing.T) {
	t.Parallel()
	bus := NewEventBus()
	subscription := bus.Subscribe()
	t.Cleanup(subscription.Close)

	bus.Publish(TopicStatus)
	bus.Publish(TopicUPs | TopicDeliveries)

	topics, revision, err := subscription.Next(t.Context())
	require.NoError(t, err)
	assert.Equal(t, TopicStatus|TopicUPs|TopicDeliveries, topics)
	assert.Equal(t, uint64(2), revision)
}
