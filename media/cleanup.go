package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrNoCleanupTask = errors.New("no media cleanup task")

type CleanupTask struct {
	ID           string
	RelativePath string
	Attempts     int
}

type CleanupStore interface {
	NextMediaCleanupTask(context.Context, time.Time) (CleanupTask, error)
	CompleteMediaCleanupTask(context.Context, string) error
	RetryMediaCleanupTask(context.Context, string, time.Time, error, bool) error
}

type Cleaner struct {
	dataDir string
	store   CleanupStore
	logger  *slog.Logger
	period  time.Duration
}

func NewCleaner(dataDir string, store CleanupStore, logger *slog.Logger) (*Cleaner, error) {
	if strings.TrimSpace(dataDir) == "" || store == nil || logger == nil {
		return nil, errors.New("media cleaner requires data directory, store, and logger")
	}
	return &Cleaner{dataDir: dataDir, store: store, logger: logger, period: 30 * time.Second}, nil
}

func (cleaner *Cleaner) Run(ctx context.Context) error {
	if err := cleaner.cleanDue(ctx, time.Now()); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	ticker := time.NewTicker(cleaner.period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if err := cleaner.cleanDue(ctx, now); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

func (cleaner *Cleaner) cleanDue(ctx context.Context, now time.Time) error {
	for range 100 {
		task, err := cleaner.store.NextMediaCleanupTask(ctx, now)
		if errors.Is(err, ErrNoCleanupTask) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("loading media cleanup task: %w", err)
		}
		permanent, cleanupErr := cleaner.remove(task.RelativePath)
		if cleanupErr == nil {
			if err := cleaner.store.CompleteMediaCleanupTask(ctx, task.ID); err != nil {
				return fmt.Errorf("completing media cleanup task %s: %w", task.ID, err)
			}
			continue
		}
		delay := min(time.Hour, time.Minute*time.Duration(1<<min(task.Attempts, 6)))
		if err := cleaner.store.RetryMediaCleanupTask(ctx, task.ID, now.Add(delay), cleanupErr, permanent); err != nil {
			return fmt.Errorf("rescheduling media cleanup task %s: %w", task.ID, err)
		}
		cleaner.logger.WarnContext(ctx, "media cleanup deferred", "event", "media.cleanup.deferred", "task_id", task.ID, "blocked", permanent, "error", cleanupErr)
	}
	return nil
}

func (cleaner *Cleaner) remove(relativePath string) (bool, error) {
	abs, err := Resolve(cleaner.dataDir, relativePath)
	if err != nil {
		return true, err
	}
	if err := rejectSymlinkPath(cleaner.dataDir, abs, true); err != nil {
		return true, err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("removing media file: %w", err)
	}
	if err := cleaner.pruneEmpty(filepath.Dir(abs)); err != nil {
		return false, err
	}
	return false, nil
}

func (cleaner *Cleaner) pruneEmpty(start string) error {
	root := filepath.Clean(filepath.Join(cleaner.dataDir, "media"))
	for current := filepath.Clean(start); current != root; current = filepath.Dir(current) {
		relative, err := filepath.Rel(root, current)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return errors.New("media cleanup directory escapes media root")
		}
		if err := rejectSymlinkPath(cleaner.dataDir, current, true); err != nil {
			return err
		}
		err = os.Remove(current)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if errors.Is(err, os.ErrExist) || errors.Is(err, syscall.ENOTEMPTY) {
			return nil
		}
		return fmt.Errorf("pruning media directory: %w", err)
	}
	return nil
}
