package media

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cleanupStoreFake struct {
	mu      sync.Mutex
	task    CleanupTask
	loaded  bool
	retried chan bool
}

func (store *cleanupStoreFake) NextMediaCleanupTask(context.Context, time.Time) (CleanupTask, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loaded {
		return CleanupTask{}, ErrNoCleanupTask
	}
	store.loaded = true
	return store.task, nil
}

func (*cleanupStoreFake) CompleteMediaCleanupTask(context.Context, string) error {
	return errors.New("unsafe cleanup unexpectedly completed")
}

func (store *cleanupStoreFake) RetryMediaCleanupTask(_ context.Context, _ string, _ time.Time, _ error, blocked bool) error {
	store.retried <- blocked
	return nil
}

func TestCleanerBlocksUnsafePathsWithoutDeletingOutsideMediaRoot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		relative   string
		setup      func(*testing.T, string) string
		wantExists string
	}{
		{
			name:     "path traversal",
			relative: "../outside.txt",
			setup: func(t *testing.T, dataDir string) string {
				path := filepath.Join(filepath.Dir(dataDir), "outside.txt")
				require.NoError(t, os.WriteFile(path, []byte("keep"), 0o600))
				return path
			},
		},
		{
			name:     "symbolic link",
			relative: "media/zsxq/source/file.txt",
			setup: func(t *testing.T, dataDir string) string {
				outside := t.TempDir()
				path := filepath.Join(outside, "file.txt")
				require.NoError(t, os.WriteFile(path, []byte("keep"), 0o600))
				require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "media", "zsxq"), 0o700))
				require.NoError(t, os.Symlink(outside, filepath.Join(dataDir, "media", "zsxq", "source")))
				return path
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			protected := tt.setup(t, dataDir)
			store := &cleanupStoreFake{task: CleanupTask{ID: "task", RelativePath: tt.relative}, retried: make(chan bool, 1)}
			cleaner, err := NewCleaner(dataDir, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- cleaner.Run(ctx) }()
			select {
			case blocked := <-store.retried:
				assert.True(t, blocked)
			case <-time.After(time.Second):
				require.FailNow(t, "cleanup task was not classified")
			}
			cancel()
			require.NoError(t, <-done)
			assert.FileExists(t, protected)
		})
	}
}

func TestCleanerRemovesFileAndPrunesOnlyEmptyParents(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "media", "zsxq", "source", "content", "file.txt")
	sibling := filepath.Join(dataDir, "media", "zsxq", "source", "keep.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
	require.NoError(t, os.WriteFile(target, []byte("remove"), 0o600))
	require.NoError(t, os.WriteFile(sibling, []byte("keep"), 0o600))
	store := &cleanupStoreFake{task: CleanupTask{ID: "task", RelativePath: "media/zsxq/source/content/file.txt"}, retried: make(chan bool, 1)}
	completed := make(chan struct{}, 1)
	cleaner := &Cleaner{dataDir: dataDir, store: cleanupCompletingStore{cleanupStoreFake: store, completed: completed}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), period: time.Hour}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- cleaner.Run(ctx) }()
	select {
	case <-completed:
	case <-time.After(time.Second):
		require.FailNow(t, "cleanup task did not complete")
	}
	cancel()
	require.NoError(t, <-done)
	assert.NoFileExists(t, target)
	assert.NoDirExists(t, filepath.Dir(target))
	assert.FileExists(t, sibling)
}

type cleanupCompletingStore struct {
	*cleanupStoreFake
	completed chan struct{}
}

func (store cleanupCompletingStore) CompleteMediaCleanupTask(context.Context, string) error {
	store.completed <- struct{}{}
	return nil
}
