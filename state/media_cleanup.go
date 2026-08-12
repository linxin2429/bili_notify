package state

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/media"
	"gorm.io/gorm"
)

func (s *Store) NextMediaCleanupTask(ctx context.Context, now time.Time) (media.CleanupTask, error) {
	var row mediaCleanupTaskRow
	err := s.db.WithContext(ctx).Where("state = ? AND next_at <= ?", "pending", now.Unix()).Order("next_at, id").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return media.CleanupTask{}, media.ErrNoCleanupTask
	}
	if err != nil {
		return media.CleanupTask{}, err
	}
	return media.CleanupTask{ID: row.ID, RelativePath: row.RelativePath, Attempts: row.Attempts}, nil
}

func (s *Store) CompleteMediaCleanupTask(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).Where("id = ?", id).Delete(&mediaCleanupTaskRow{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RetryMediaCleanupTask(ctx context.Context, id string, next time.Time, taskErr error, blocked bool) error {
	state := "pending"
	if blocked {
		state = "blocked"
	}
	message := strings.TrimSpace(taskErr.Error())
	if len(message) > 1000 {
		message = message[:1000]
	}
	result := s.db.WithContext(ctx).Model(&mediaCleanupTaskRow{}).Where("id = ?", id).Updates(map[string]any{
		"state": state, "attempts": gorm.Expr("attempts + 1"), "next_at": next.Unix(), "last_error": message, "updated_at": time.Now().Unix(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
