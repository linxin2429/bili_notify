package web

import (
	"slices"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMicrosoftIdentityChanged(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current map[string]string
		update  map[string]string
		want    bool
	}{
		{
			name:    "blank tenant equals common",
			current: map[string]string{"client_id": "client", "tenant": ""},
			update:  map[string]string{"client_id": "client", "tenant": "common"},
		},
		{
			name:    "client changed",
			current: map[string]string{"client_id": "old", "tenant": "common"},
			update:  map[string]string{"client_id": "new", "tenant": "common"},
			want:    true,
		},
		{
			name:    "tenant changed",
			current: map[string]string{"client_id": "client", "tenant": "common"},
			update:  map[string]string{"client_id": "client", "tenant": "consumers"},
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, microsoftIdentityChanged(tt.current, tt.update))
		})
	}
}

func TestChannelViewNeverReturnsSecrets(t *testing.T) {
	t.Parallel()
	view := toChannelView(model.Channel{
		ID: "channel", Name: "mail", Type: model.ChannelEmail, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Settings: map[string]string{"host": "smtp.example.com", "password": "secret", "webhook": "https://secret"},
	})
	assert.Empty(t, view.Settings["password"])
	assert.Empty(t, view.Settings["webhook"])
	assert.Equal(t, "smtp.example.com", view.Settings["host"])
	require.True(t, slices.Equal(view.ConfiguredSecrets, []string{"password", "webhook"}))
}
