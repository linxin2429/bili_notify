package state

import (
	"errors"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FeedState struct {
	AccountUID     string
	UpdateBaseline string
	Initialized    bool
	UpdatedAt      time.Time
}

type FollowRelation struct {
	AccountUID      string
	UPUID           string
	State           model.FollowState
	SpaceSynced     bool
	CheckedAt       time.Time
	LastSpacePollAt time.Time
}

func (s *Store) CurrentAccount() (model.BiliAccount, error) {
	session, err := s.Session()
	if err != nil {
		return model.BiliAccount{}, err
	}
	if session.AccountUID == "" {
		return model.BiliAccount{}, ErrNotFound
	}
	return model.BiliAccount{UID: session.AccountUID, Name: session.AccountName}, nil
}

func (s *Store) FeedState(accountUID string) (FeedState, error) {
	var row biliFeedStateRow
	err := s.db.Where("account_uid = ?", accountUID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return FeedState{}, ErrNotFound
	}
	if err != nil {
		return FeedState{}, err
	}
	return FeedState{AccountUID: row.AccountUID, UpdateBaseline: row.UpdateBaseline, Initialized: row.Initialized != 0, UpdatedAt: time.Unix(row.UpdatedAt, 0)}, nil
}

func (s *Store) InitializeFeed(accountUID, baseline string, at time.Time) error {
	if accountUID == "" || baseline == "" {
		return errors.New("account uid and update baseline are required")
	}
	row := biliFeedStateRow{AccountUID: accountUID, UpdateBaseline: baseline, Initialized: 1, UpdatedAt: at.Unix()}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "account_uid"}},
		DoUpdates: clause.AssignmentColumns([]string{"update_baseline", "initialized", "updated_at"}),
	}).Create(&row).Error
}

func (s *Store) ResetFeed(accountUID string, upUIDs []string, at time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		row := biliFeedStateRow{AccountUID: accountUID, UpdatedAt: at.Unix()}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_uid"}},
			DoUpdates: clause.AssignmentColumns([]string{"update_baseline", "initialized", "updated_at"}),
		}).Create(&row).Error; err != nil {
			return err
		}
		if len(upUIDs) == 0 {
			return nil
		}
		return tx.Model(&upFollowRelationRow{}).
			Where("account_uid = ? AND up_uid IN ?", accountUID, upUIDs).
			Update("space_synced", 0).Error
	})
}

func (s *Store) FollowRelations(accountUID string) (map[string]FollowRelation, error) {
	var rows []upFollowRelationRow
	if err := s.db.Where("account_uid = ?", accountUID).Find(&rows).Error; err != nil {
		return nil, err
	}
	relations := make(map[string]FollowRelation, len(rows))
	for _, row := range rows {
		relation := FollowRelation{
			AccountUID: row.AccountUID, UPUID: row.UPUID, State: model.FollowState(row.FollowState), SpaceSynced: row.SpaceSynced != 0,
		}
		if row.CheckedAt != nil {
			relation.CheckedAt = time.Unix(*row.CheckedAt, 0)
		}
		if row.LastSpacePollAt != nil {
			relation.LastSpacePollAt = time.Unix(*row.LastSpacePollAt, 0)
		}
		relations[row.UPUID] = relation
	}
	return relations, nil
}

func (s *Store) PutFollowRelations(accountUID string, states map[string]model.FollowState, checkedAt time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		stamp := checkedAt.Unix()
		for uid, state := range states {
			row := upFollowRelationRow{AccountUID: accountUID, UPUID: uid, FollowState: string(state), CheckedAt: &stamp}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "account_uid"}, {Name: "up_uid"}},
				DoUpdates: clause.Assignments(map[string]any{"follow_state": string(state), "checked_at": stamp}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) MarkSpaceSynced(accountUID, upUID string, at time.Time) error {
	stamp := at.Unix()
	row := upFollowRelationRow{AccountUID: accountUID, UPUID: upUID, FollowState: string(model.FollowUnknown), SpaceSynced: 1, LastSpacePollAt: &stamp}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "account_uid"}, {Name: "up_uid"}},
		DoUpdates: clause.Assignments(map[string]any{"space_synced": 1, "last_space_poll_at": stamp}),
	}).Create(&row).Error
}

func (s *Store) SetUPResults(uids []string, at time.Time, pollErr error) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, uid := range uids {
			var row sourceRow
			if err := tx.Where("id = ?", model.SourceID(model.PlatformBilibili, uid)).Take(&row).Error; err != nil {
				return err
			}
			up := upFromSourceRow(row)
			up.LastPollAt = at
			if pollErr == nil {
				up.LastSuccessAt = at
				up.LastError = ""
				up.ConsecutiveFail = 0
			} else {
				up.LastError = pollErr.Error()
				up.ConsecutiveFail++
			}
			sourceUpdates := map[string]any{"last_poll_at": at.Unix(), "last_error": up.LastError, "consecutive_fails": up.ConsecutiveFail}
			if pollErr == nil {
				sourceUpdates["last_success_at"] = at.Unix()
			}
			if err := tx.Model(&sourceRow{}).Where("id = ?", model.SourceID(model.PlatformBilibili, uid)).Updates(sourceUpdates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) enrichUPRouting(ups []model.UP) error {
	if len(ups) == 0 {
		return nil
	}
	account, err := s.CurrentAccount()
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	relations, err := s.FollowRelations(account.UID)
	if err != nil {
		return err
	}
	feed, feedErr := s.FeedState(account.UID)
	if feedErr != nil && !errors.Is(feedErr, ErrNotFound) {
		return feedErr
	}
	for i := range ups {
		relation, ok := relations[ups[i].UID]
		if !ok {
			continue
		}
		ups[i].FollowState = relation.State
		ups[i].FollowCheckedAt = relation.CheckedAt
		if relation.State == model.Followed && relation.SpaceSynced && feed.Initialized && ups[i].BaselineReady && ups[i].ExclusiveBaselineReady {
			ups[i].CollectionRoute = model.CollectionRouteFeedAll
		}
	}
	return nil
}
