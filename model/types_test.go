package model

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validSettings(pollSec int, rate float64, concurrency int) RuntimeSettings {
	settings := DefaultRuntimeSettings()
	settings.PollIntervalSec = pollSec
	settings.RequestRate = rate
	settings.RequestConcurrency = concurrency
	return settings
}

func TestRuntimeSettingsExtendedValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*RuntimeSettings)
		wantErr string
	}{
		{name: "poll too long", mutate: func(s *RuntimeSettings) { s.PollIntervalSec = MaxPollIntervalSec + 1 }, wantErr: "poll interval"},
		{name: "rate nan", mutate: func(s *RuntimeSettings) { s.RequestRate = math.NaN() }, wantErr: "request rate"},
		{name: "comment batch too long", mutate: func(s *RuntimeSettings) { s.CommentBatchIntervalSec = MaxCommentBatchIntervalSec + 1 }, wantErr: "comment_batch_interval_sec"},
		{name: "bad log level", mutate: func(s *RuntimeSettings) { s.LogLevel = "trace" }, wantErr: "log_level"},
		{name: "audit retention too long", mutate: func(s *RuntimeSettings) { s.AuditLogRetentionDays = MaxLogRetentionDays + 1 }, wantErr: "audit_log_retention_days"},
		{name: "system retention too short", mutate: func(s *RuntimeSettings) { s.SystemLogRetentionDays = 0 }, wantErr: "system_log_retention_days"},
		{name: "relation refresh too short", mutate: func(s *RuntimeSettings) { s.RelationRefreshSec = MinRelationRefreshSec - 1 }, wantErr: "relation_refresh_interval_sec"},
		{name: "space reconcile too long", mutate: func(s *RuntimeSettings) { s.SpaceReconcileSec = MaxSpaceReconcileSec + 1 }, wantErr: "space_reconcile_interval_sec"},
		{name: "dynamic pages too high", mutate: func(s *RuntimeSettings) { s.MaxDynamicPages = MaxDynamicPages + 1 }, wantErr: "max_dynamic_pages"},
		{name: "risk pause too short", mutate: func(s *RuntimeSettings) { s.RiskPauseSec = MinRiskPauseSec - 1 }, wantErr: "risk_pause_sec"},
		{name: "delivery concurrency too high", mutate: func(s *RuntimeSettings) { s.DeliveryConcurrency = MaxDeliveryConcurrency + 1 }, wantErr: "delivery_concurrency"},
		{name: "backlog count too low", mutate: func(s *RuntimeSettings) { s.BacklogAlertCount = 0 }, wantErr: "backlog_alert_count"},
		{name: "backlog age too high", mutate: func(s *RuntimeSettings) { s.BacklogAlertAgeSec = MaxBacklogAlertAgeSec + 1 }, wantErr: "backlog_alert_age_sec"},
		{name: "retry delay out of range", mutate: func(s *RuntimeSettings) { s.DeliveryRetryDelaysSec[0] = 0 }, wantErr: "delivery_retry_delays_sec[0]"},
		{name: "retry delays decreasing", mutate: func(s *RuntimeSettings) { s.DeliveryRetryDelaysSec[2] = 10 }, wantErr: "nondecreasing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			settings := DefaultRuntimeSettings()
			tt.mutate(&settings)
			err := settings.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
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
			wantErr:  "poll interval must be between",
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

func TestFeishuAppCredentialsValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings map[string]string
		wantErr  string
	}{
		{
			name:     "webhook only",
			settings: map[string]string{"webhook": "https://open.feishu.cn/hook", "secret": "s"},
		},
		{
			name:     "paired app credentials",
			settings: map[string]string{"webhook": "https://open.feishu.cn/hook", "secret": "s", "app_id": "cli_a", "app_secret": "sec"},
		},
		{
			name:     "app_id alone",
			settings: map[string]string{"webhook": "https://open.feishu.cn/hook", "secret": "s", "app_id": "cli_a"},
			wantErr:  "app_id and app_secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Channel{Name: "feishu", Type: ChannelFeishu, Settings: tt.settings}.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
