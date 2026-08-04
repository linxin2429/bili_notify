package model

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeSettingsValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings RuntimeSettings
		wantErr  string
	}{
		{
			name:     "valid defaults",
			settings: RuntimeSettings{PollIntervalSec: 30, RequestRate: 2, RequestConcurrency: 4},
		},
		{
			name:     "minimum bounds",
			settings: RuntimeSettings{PollIntervalSec: 10, RequestRate: 0.1, RequestConcurrency: 1},
		},
		{
			name:     "maximum bounds",
			settings: RuntimeSettings{PollIntervalSec: 3600, RequestRate: 10, RequestConcurrency: 16},
		},
		{
			name:     "poll too short",
			settings: RuntimeSettings{PollIntervalSec: 9, RequestRate: 2, RequestConcurrency: 4},
			wantErr:  "poll interval must be at least 10s",
		},
		{
			name:     "rate zero",
			settings: RuntimeSettings{PollIntervalSec: 30, RequestRate: 0, RequestConcurrency: 4},
			wantErr:  "request rate must be in (0, 10]",
		},
		{
			name:     "rate too high",
			settings: RuntimeSettings{PollIntervalSec: 30, RequestRate: 10.1, RequestConcurrency: 4},
			wantErr:  "request rate must be in (0, 10]",
		},
		{
			name:     "concurrency too low",
			settings: RuntimeSettings{PollIntervalSec: 30, RequestRate: 2, RequestConcurrency: 0},
			wantErr:  "request concurrency must be in [1, 16]",
		},
		{
			name:     "concurrency too high",
			settings: RuntimeSettings{PollIntervalSec: 30, RequestRate: 2, RequestConcurrency: 17},
			wantErr:  "request concurrency must be in [1, 16]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.settings.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, time.Duration(tt.settings.PollIntervalSec)*time.Second, tt.settings.PollInterval())
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateCollectorParamsJoinsErrors(t *testing.T) {
	t.Parallel()
	err := ValidateCollectorParams(time.Second, 0, 0)
	require.Error(t, err)
	assert.ErrorContains(t, err, "poll interval")
	assert.ErrorContains(t, err, "request rate")
	assert.ErrorContains(t, err, "request concurrency")
	// errors.Join produces a multi-error; ensure more than one branch is present.
	assert.GreaterOrEqual(t, strings.Count(err.Error(), "\n")+1, 3)
}

func TestMicrosoftChannelValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		channel Channel
		wantErr string
	}{
		{
			name: "authorized valid",
			channel: Channel{
				Name: "outlook", Type: ChannelMicrosoft,
				Settings: map[string]string{
					"client_id": "11111111-2222-3333-4444-555555555555",
					"tenant":    "common", "to": "one@example.com,Two <two@example.com>",
					"access_token": "access", "refresh_token": "refresh",
				},
			},
		},
		{
			name: "invalid tenant",
			channel: Channel{
				Name: "outlook", Type: ChannelMicrosoft,
				Settings: map[string]string{
					"client_id": "11111111-2222-3333-4444-555555555555",
					"tenant":    "../token", "to": "one@example.com",
				},
			},
			wantErr: "microsoft tenant",
		},
		{
			name: "must authorize before enable",
			channel: Channel{
				Name: "outlook", Type: ChannelMicrosoft, Enabled: true,
				Settings: map[string]string{
					"client_id": "11111111-2222-3333-4444-555555555555",
					"tenant":    "common", "to": "one@example.com",
				},
			},
			wantErr: "must be authorized",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.channel.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
