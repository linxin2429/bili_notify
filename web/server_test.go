package web

import (
	"slices"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
)

func TestMicrosoftIdentityChanged(t *testing.T) {
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
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := microsoftIdentityChanged(test.current, test.update); got != test.want {
				t.Fatalf("microsoftIdentityChanged() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestChannelViewNeverReturnsSecrets(t *testing.T) {
	view := toChannelView(model.Channel{
		ID: "channel", Name: "mail", Type: model.ChannelEmail, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Settings: map[string]string{"host": "smtp.example.com", "password": "secret", "webhook": "https://secret"},
	})
	if view.Settings["password"] != "" || view.Settings["webhook"] != "" {
		t.Fatalf("secret settings leaked: %#v", view.Settings)
	}
	if !slices.Equal(view.ConfiguredSecrets, []string{"password", "webhook"}) {
		t.Fatalf("configured secrets=%v", view.ConfiguredSecrets)
	}
}
