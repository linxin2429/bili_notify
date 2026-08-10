package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditLogQuery(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, time.August, 6, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		query AuditQuery
		want  string
	}{
		{name: "newest first", query: AuditQuery{}, want: "up.create"},
		{name: "action", query: AuditQuery{Action: "auth.login"}, want: "auth.login"},
		{name: "outcome", query: AuditQuery{Outcome: AuditSuccess}, want: "up.create"},
		{name: "resource", query: AuditQuery{ResourceType: "up"}, want: "up.create"},
		{name: "search resource id", query: AuditQuery{Q: "42"}, want: "up.create"},
		{name: "half open time", query: AuditQuery{From: base.Add(time.Minute), To: base.Add(2 * time.Minute)}, want: "up.create"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := seedAuditStore(t, base)
			items, total, err := store.QueryAuditLogs(tt.query)
			require.NoError(t, err)
			require.NotEmpty(t, items)
			assert.Equal(t, len(items), total)
			assert.Equal(t, tt.want, items[0].Action)
		})
	}
}

func TestAuditLogPrune(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, time.August, 6, 8, 0, 0, 0, time.UTC)
	store := seedAuditStore(t, base)
	deleted, err := store.PruneAuditLogs(base.Add(30*time.Second), 1000)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	items, total, err := store.QueryAuditLogs(AuditQuery{})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, items, 1)
	assert.Equal(t, "up.create", items[0].Action)
}

func seedAuditStore(t *testing.T, base time.Time) *Store {
	t.Helper()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 72))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	entries := []AuditLog{
		{OccurredAt: base, RequestID: "request-1", Actor: "anonymous", RemoteIP: "192.0.2.1", Action: "auth.login", Outcome: AuditDenied, HTTPMethod: "POST", Route: "/api/v3/session", StatusCode: 401, ErrorCode: "invalid_credentials"},
		{OccurredAt: base.Add(time.Minute), RequestID: "request-2", Actor: "administrator", SessionID: "session", RemoteIP: "192.0.2.2", Action: "up.create", ResourceType: "up", ResourceID: "42", Outcome: AuditSuccess, HTTPMethod: "POST", Route: "/api/v3/ups", StatusCode: 201, Details: map[string]any{"enabled": true}},
	}
	for _, entry := range entries {
		_, err := store.AppendAudit(entry)
		require.NoError(t, err)
	}
	return store
}
