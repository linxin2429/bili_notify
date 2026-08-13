package state

import (
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV13MalformedLegacyPayloadBecomesExplicitBlockedSystemDelivery(t *testing.T) {
	t.Parallel()
	row := v13OutboxRow{ID: "broken", ChannelID: "channel", State: string(model.DeliveryPending), Attempts: 2,
		NextAt: time.Now().Unix(), CreatedAt: time.Now().Unix(), PayloadJSON: `{not-json}`}
	_, err := v13Delivery(row)
	require.Error(t, err)
	delivery := blockedConversionDelivery(row, err)
	assert.Equal(t, model.DeliveryKindSystem, delivery.Kind)
	assert.Equal(t, model.DeliveryBlocked, delivery.State)
	require.NotNil(t, delivery.System)
	assert.Contains(t, delivery.System.Body, "could not be converted")
	assert.Contains(t, delivery.LastError, "could not be converted")
}
