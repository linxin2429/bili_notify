package state

import (
	"errors"
	"fmt"
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
			var row upRow
			if err := tx.Where("uid = ?", uid).Take(&row).Error; err != nil {
				return err
			}
			up := row.toModel()
			up.LastPollAt = at
			if pollErr == nil {
				up.LastSuccessAt = at
				up.LastError = ""
				up.ConsecutiveFail = 0
			} else {
				up.LastError = pollErr.Error()
				up.ConsecutiveFail++
			}
			updated := upFromModel(up)
			if err := tx.Save(&updated).Error; err != nil {
				return err
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

func (s *Store) RecordFeedDynamics(accountUID, baseline string, dynamics []model.Dynamic, channelIDs, failedUIDs []string) (int, error) {
	if accountUID == "" || baseline == "" {
		return 0, errors.New("account uid and update baseline are required")
	}
	created := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		autoAI, err := automaticAIEnabledTx(tx)
		if err != nil {
			return err
		}
		if err := archiveDynamicsTx(tx, dynamics, DynamicBaselineNone); err != nil {
			return err
		}
		now := time.Now()
		for _, dynamic := range dynamics {
			if dynamic.ID == "" || dynamic.UID == "" {
				continue
			}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seenDynamicRow{UID: dynamic.UID, DynamicID: dynamic.ID, SeenAt: dynamic.PublishedAt.Unix()})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			origin := originTraceparent(tx.Statement.Context)
			for _, channelID := range channelIDs {
				delivery := model.Delivery{
					ID: dynamic.ID + ":" + channelID, Kind: model.DeliveryKindDynamic, Dynamic: dynamic,
					ChannelID: channelID, State: model.DeliveryPending, NextAt: now, CreatedAt: now,
					OriginTraceparent: origin,
				}
				if err := putDeliveryTx(tx, delivery); err != nil {
					return err
				}
			}
			if autoAI {
				if _, err := s.createAutomaticAIJobsTx(tx, dynamic, channelIDs); err != nil {
					return fmt.Errorf("creating automatic AI pipeline for dynamic %s: %w", dynamic.ID, err)
				}
			}
			created++
		}
		feed := biliFeedStateRow{AccountUID: accountUID, UpdateBaseline: baseline, Initialized: 1, UpdatedAt: now.Unix()}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_uid"}},
			DoUpdates: clause.AssignmentColumns([]string{"update_baseline", "initialized", "updated_at"}),
		}).Create(&feed).Error; err != nil {
			return err
		}
		if len(failedUIDs) > 0 {
			if err := tx.Model(&upFollowRelationRow{}).
				Where("account_uid = ? AND up_uid IN ?", accountUID, failedUIDs).
				Update("space_synced", 0).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("recording feed dynamics: %w", err)
	}
	return created, nil
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
