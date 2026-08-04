package model

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validSettings(pollSec int, rate float64, concurrency int) RuntimeSettings {
	trackN, rootPages, replyPages, batchSec, enabled := DefaultCommentSettings()
	return RuntimeSettings{
		PollIntervalSec:         pollSec,
		RequestRate:             rate,
		RequestConcurrency:      concurrency,
		CommentEnabled:          enabled,
		CommentTrackN:           trackN,
		CommentRootPages:        rootPages,
		CommentReplyPages:       replyPages,
		CommentBatchIntervalSec: batchSec,
	}
}

func TestRuntimeSettingsValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings RuntimeSettings
		wantErr  string
	}{
		{
			name:     "valid defaults",
			settings: validSettings(30, 2, 4),
		},
		{
			name:     "minimum bounds",
			settings: validSettings(10, 0.1, 1),
		},
		{
			name:     "maximum bounds",
			settings: validSettings(3600, 10, 16),
		},
		{
			name:     "poll too short",
			settings: validSettings(9, 2, 4),
			wantErr:  "poll interval must be at least 10s",
		},
		{
			name:     "rate zero",
			settings: validSettings(30, 0, 4),
			wantErr:  "request rate must be in (0, 10]",
		},
		{
			name:     "rate too high",
			settings: validSettings(30, 10.1, 4),
			wantErr:  "request rate must be in (0, 10]",
		},
		{
			name:     "concurrency too low",
			settings: validSettings(30, 2, 0),
			wantErr:  "request concurrency must be in [1, 16]",
		},
		{
			name:     "concurrency too high",
			settings: validSettings(30, 2, 17),
			wantErr:  "request concurrency must be in [1, 16]",
		},
		{
			name: "comment track n too high",
			settings: func() RuntimeSettings {
				s := validSettings(30, 2, 4)
				s.CommentTrackN = MaxCommentTrackN + 1
				return s
			}(),
			wantErr: "comment_track_n",
		},
		{
			name: "comment batch too short",
			settings: func() RuntimeSettings {
				s := validSettings(30, 2, 4)
				s.CommentBatchIntervalSec = MinCommentBatchIntervalSec - 1
				return s
			}(),
			wantErr: "comment_batch_interval_sec",
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
