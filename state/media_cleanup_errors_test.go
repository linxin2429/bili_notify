package state

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMediaCleanupMissingAndCanceled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		canceled bool
		op       string
		want     error
	}{
		{name: "missing next", op: "next", want: media.ErrNoCleanupTask},
		{name: "missing completion", op: "complete", want: ErrNotFound},
		{name: "missing retry", op: "retry", want: ErrNotFound},
		{name: "canceled next", op: "next", canceled: true, want: context.Canceled},
		{name: "canceled completion", op: "complete", canceled: true, want: context.Canceled},
		{name: "canceled retry", op: "retry", canceled: true, want: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 37)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tt.canceled {
				cancel()
			}
			var err error
			switch tt.op {
			case "next":
				var task media.CleanupTask
				task, err = store.NextMediaCleanupTask(ctx, time.Now())
				assert.Empty(t, task.ID)
			case "complete":
				err = store.CompleteMediaCleanupTask(ctx, "missing")
			case "retry":
				err = store.RetryMediaCleanupTask(ctx, "missing", time.Now(), errors.New("busy"), false)
			}
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestMediaCleanupBlockedTaskIsNotRetried(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 38)
	require.NoError(t, store.db.Exec(`INSERT INTO media_cleanup_tasks(id,relative_path,state,next_at,created_at,updated_at) VALUES('task','media/file','pending',0,0,0)`).Error)
	require.NoError(t, store.RetryMediaCleanupTask(t.Context(), "task", time.Now(), errors.New("  "+strings.Repeat("x", 1100)+"  "), true))
	_, err := store.NextMediaCleanupTask(t.Context(), time.Now().Add(time.Hour))
	require.ErrorIs(t, err, media.ErrNoCleanupTask)
	var row mediaCleanupTaskRow
	require.NoError(t, store.db.Take(&row).Error)
	assert.Equal(t, "blocked", row.State)
	assert.Equal(t, 1, row.Attempts)
	assert.Len(t, row.LastError, 1000)
}
