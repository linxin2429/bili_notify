package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
	platformcontract "github.com/linxin2429/bili_notify/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const tablePlatformAccounts = "platform_accounts"

type platformAccountRow struct {
	Platform        string `gorm:"column:platform;primaryKey"`
	ExternalID      string `gorm:"column:external_id"`
	DisplayName     string `gorm:"column:display_name"`
	MaskedPhone     string `gorm:"column:masked_phone"`
	Status          string `gorm:"column:status"`
	SealedSession   []byte `gorm:"column:sealed_session"`
	SealedAAD       string `gorm:"column:sealed_aad"`
	VerifiedAt      *int64 `gorm:"column:verified_at"`
	UpdatedAt       int64  `gorm:"column:updated_at"`
	RiskPausedUntil *int64 `gorm:"column:risk_paused_until"`
	LastError       string `gorm:"column:last_error"`
}

func (platformAccountRow) TableName() string { return tablePlatformAccounts }

// PutPlatformAccount encrypts session cookies and atomically replaces the
// account projection. Passing a nil Session preserves an existing secret.
func (s *Store) PutPlatformAccount(account model.PlatformAccount) error {
	if err := account.Validate(); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var sealed []byte
		if account.Session != nil {
			var err error
			sealed, err = sealJSON(s.vault, tablePlatformAccounts, string(account.Platform), account.Session)
			if err != nil {
				return fmt.Errorf("encrypting %s session: %w", account.Platform, err)
			}
		} else {
			var old platformAccountRow
			if err := tx.Select("sealed_session").Where("platform = ?", account.Platform).Take(&old).Error; err == nil {
				sealed = old.SealedSession
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		now := account.UpdatedAt
		if now.IsZero() {
			now = time.Now()
		}
		row := platformAccountRow{
			Platform: string(account.Platform), ExternalID: account.ExternalID,
			DisplayName: account.DisplayName, MaskedPhone: account.MaskedPhone,
			Status: string(account.Status), SealedSession: sealed,
			SealedAAD: tablePlatformAccounts, UpdatedAt: now.Unix(), LastError: account.LastError,
		}
		row.VerifiedAt = unixPointer(account.VerifiedAt)
		row.RiskPausedUntil = unixPointer(account.RiskPausedUntil)
		return tx.Save(&row).Error
	})
}

func (s *Store) PlatformAccount(platform model.Platform) (model.PlatformAccount, error) {
	if err := platform.Validate(); err != nil {
		return model.PlatformAccount{}, err
	}
	var row platformAccountRow
	err := s.db.Where("platform = ?", platform).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.PlatformAccount{}, ErrNotFound
	}
	if err != nil {
		return model.PlatformAccount{}, err
	}
	account := platformAccountModel(row)
	if len(row.SealedSession) != 0 {
		if row.SealedAAD != tablePlatformAccounts {
			return model.PlatformAccount{}, fmt.Errorf("account %s has invalid sealed AAD %q", platform, row.SealedAAD)
		}
		if err := openJSON(s.vault, tablePlatformAccounts, string(platform), row.SealedSession, &account.Session); err != nil {
			return model.PlatformAccount{}, err
		}
	}
	if platform == model.PlatformZSXQ && account.Status == model.AccountConnected && strings.TrimSpace(account.Session["zsxq_access_token"]) == "" {
		account.Status = model.AccountInvalid
		account.LastError = "session import required"
	}
	return account, nil
}

func (s *Store) ListPlatformAccounts() ([]model.PlatformAccount, error) {
	platforms := platformcontract.BuiltinPlatforms()
	accounts := make([]model.PlatformAccount, 0, len(platforms))
	for _, platform := range platforms {
		account, err := s.PlatformAccount(platform)
		if errors.Is(err, ErrNotFound) {
			accounts = append(accounts, model.PlatformAccount{Platform: platform, Status: model.AccountDisconnected})
			continue
		}
		if err != nil {
			return nil, err
		}
		account.Session = nil
		accounts = append(accounts, account)
	}
	return accounts, nil
}

func (s *Store) DeletePlatformAccount(platform model.Platform) error {
	if err := platform.Validate(); err != nil {
		return err
	}
	result := s.db.Where("platform = ?", platform).Delete(&platformAccountRow{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPlatformAccountStatus updates health without touching the encrypted
// session. A missing account is intentionally ignored: disconnected accounts
// are represented by absence rather than an empty database row.
func (s *Store) SetPlatformAccountStatus(platform model.Platform, status model.AccountStatus, lastError string) error {
	if err := platform.Validate(); err != nil {
		return err
	}
	return s.db.Model(&platformAccountRow{}).
		Where("platform = ?", platform).
		Updates(map[string]any{
			"status":     string(status),
			"last_error": lastError,
			"updated_at": time.Now().Unix(),
		}).Error
}

func platformAccountModel(row platformAccountRow) model.PlatformAccount {
	account := model.PlatformAccount{
		Platform: model.Platform(row.Platform), ExternalID: row.ExternalID,
		DisplayName: row.DisplayName, MaskedPhone: row.MaskedPhone,
		Status: model.AccountStatus(row.Status), UpdatedAt: time.Unix(row.UpdatedAt, 0), LastError: row.LastError,
	}
	account.VerifiedAt = pointerTime(row.VerifiedAt)
	account.RiskPausedUntil = pointerTime(row.RiskPausedUntil)
	return account
}

type sourceRow struct {
	ID                     string `gorm:"column:id;primaryKey"`
	Platform               string `gorm:"column:platform"`
	Type                   string `gorm:"column:type"`
	ExternalID             string `gorm:"column:external_id"`
	Name                   string `gorm:"column:name"`
	Note                   string `gorm:"column:note"`
	OwnerID                string `gorm:"column:owner_id"`
	OwnerName              string `gorm:"column:owner_name"`
	ZSXQTopicMode          string `gorm:"column:zsxq_topic_mode"`
	ZSXQAuthorsJSON        string `gorm:"column:zsxq_authors_json"`
	Enabled                int    `gorm:"column:enabled"`
	BaselineState          string `gorm:"column:baseline_state"`
	BackfillCursor         string `gorm:"column:backfill_cursor"`
	HighWatermark          string `gorm:"column:high_watermark"`
	BackfillDone           int64  `gorm:"column:backfill_done"`
	BackfillTotal          int64  `gorm:"column:backfill_total"`
	LastPollAt             *int64 `gorm:"column:last_poll_at"`
	LastSuccessAt          *int64 `gorm:"column:last_success_at"`
	LastCommentAt          *int64 `gorm:"column:last_comment_at"`
	SyncLagSec             int64  `gorm:"column:sync_lag_sec"`
	LastError              string `gorm:"column:last_error"`
	ConsecutiveFails       int    `gorm:"column:consecutive_fails"`
	ExclusiveBaselineReady int    `gorm:"column:exclusive_baseline_ready"`
}

func (sourceRow) TableName() string { return "sources" }

func (s *Store) PutSource(source model.Source) error {
	if source.BaselineState == "" {
		source.BaselineState = model.BaselinePending
	}
	if source.Platform == model.PlatformZSXQ && source.ZSXQTopicMode == "" {
		source.ZSXQTopicMode = model.ZSXQTopicAll
	}
	if err := source.Validate(); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		wasEnabled := false
		var existing sourceRow
		err := tx.Where("id = ?", source.ID).Take(&existing).Error
		if err == nil {
			wasEnabled = existing.Enabled != 0
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Save(sourceFromModel(source)).Error; err != nil {
			return err
		}
		if source.Platform == model.PlatformBilibili && !wasEnabled && source.Enabled {
			return tx.Model(&upFollowRelationRow{}).Where("up_uid = ?", source.ExternalID).Update("space_synced", 0).Error
		}
		return nil
	})
}

// CreateSource inserts one new source while enforcing platform identity and
// the configured Bilibili source limit in the same transaction.
func (s *Store) CreateSource(source model.Source) error {
	if source.BaselineState == "" {
		source.BaselineState = model.BaselinePending
	}
	if source.Platform == model.PlatformZSXQ && source.ZSXQTopicMode == "" {
		source.ZSXQTopicMode = model.ZSXQTopicAll
	}
	if err := source.Validate(); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&sourceRow{}).Where("id = ?", source.ID).Count(&existing).Error; err != nil {
			return err
		}
		if existing != 0 {
			return ErrSourceExists
		}
		if source.Platform == model.PlatformBilibili {
			var total int64
			if err := tx.Model(&sourceRow{}).Where("platform = ?", model.PlatformBilibili).Count(&total).Error; err != nil {
				return err
			}
			if total >= 100 {
				return errors.New("at most 100 UPs can be configured")
			}
		}
		return tx.Create(sourceFromModel(source)).Error
	})
}

func (s *Store) Source(id string) (model.Source, error) {
	var row sourceRow
	err := s.db.Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Source{}, ErrNotFound
	}
	return row.toModel(), err
}

func (s *Store) ListSources(platform model.Platform) ([]model.Source, error) {
	query := s.db.Order("platform, name, id")
	if platform != "" {
		if err := platform.Validate(); err != nil {
			return nil, err
		}
		query = query.Where("platform = ?", platform)
	}
	var rows []sourceRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]model.Source, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toModel())
	}
	return items, nil
}

func (s *Store) DeleteSource(id string) error {
	uid := ""
	if strings.HasPrefix(id, "bilibili:up:") {
		uid = strings.TrimPrefix(id, "bilibili:up:")
	}
	return s.deleteSource(id, uid)
}

func (s *Store) deleteSource(id, bilibiliUID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var paths []string
		if err := tx.Table("attachments").Joins("JOIN contents ON contents.id = attachments.content_id").
			Where("contents.source_id = ? AND attachments.local_path != ''", id).Pluck("attachments.local_path", &paths).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		for _, relativePath := range paths {
			if err := validateStoredRelativePath(relativePath); err != nil {
				return fmt.Errorf("invalid localized attachment path %q: %w", relativePath, err)
			}
			task := mediaCleanupTaskRow{ID: stableHash("media-cleanup", relativePath), RelativePath: relativePath,
				State: "pending", NextAt: now, CreatedAt: now, UpdatedAt: now}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&task).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("source_id = ?", id).Delete(&outboxRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&seenItemRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_content_id IN (SELECT id FROM contents WHERE source_id = ?)", id).Delete(&aiJobRow{}).Error; err != nil {
			return err
		}
		if bilibiliUID != "" {
			if err := tx.Where("up_uid = ?", bilibiliUID).Delete(&upFollowRelationRow{}).Error; err != nil {
				return err
			}
		}
		result := tx.Where("id = ?", id).Delete(&sourceRow{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

type mediaCleanupTaskRow struct {
	ID           string `gorm:"column:id;primaryKey"`
	RelativePath string `gorm:"column:relative_path"`
	State        string `gorm:"column:state"`
	Attempts     int    `gorm:"column:attempts"`
	NextAt       int64  `gorm:"column:next_at"`
	LastError    string `gorm:"column:last_error"`
	CreatedAt    int64  `gorm:"column:created_at"`
	UpdatedAt    int64  `gorm:"column:updated_at"`
}

func (mediaCleanupTaskRow) TableName() string { return "media_cleanup_tasks" }

func validateStoredRelativePath(value string) error {
	if value == "" || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return errors.New("path must be relative")
	}
	for _, segment := range strings.FieldsFunc(strings.ReplaceAll(value, `\`, "/"), func(r rune) bool { return r == '/' }) {
		if segment == ".." {
			return errors.New("path traversal is not allowed")
		}
	}
	return nil
}

func sourceFromModel(source model.Source) sourceRow {
	if source.ZSXQAuthors == nil {
		source.ZSXQAuthors = []model.ZSXQAuthor{}
	}
	authorsJSON, _ := json.Marshal(source.ZSXQAuthors)
	return sourceRow{
		ID: source.ID, Platform: string(source.Platform), Type: string(source.Type), ExternalID: source.ExternalID,
		Name: source.Name, Note: source.Note, OwnerID: source.OwnerID, OwnerName: source.OwnerName,
		ZSXQTopicMode: string(source.ZSXQTopicMode), ZSXQAuthorsJSON: string(authorsJSON),
		Enabled: boolToInt(source.Enabled), BaselineState: string(source.BaselineState), BackfillCursor: source.BackfillCursor,
		HighWatermark: source.HighWatermark, BackfillDone: source.BackfillDone, BackfillTotal: source.BackfillTotal,
		LastPollAt: unixPointer(source.LastPollAt), LastSuccessAt: unixPointer(source.LastSuccessAt), LastCommentAt: unixPointer(source.LastCommentAt),
		SyncLagSec: source.SyncLagSec, LastError: source.LastError, ConsecutiveFails: source.ConsecutiveFails,
	}
}

func (r sourceRow) toModel() model.Source {
	var authors []model.ZSXQAuthor
	_ = json.Unmarshal([]byte(r.ZSXQAuthorsJSON), &authors)
	return model.Source{
		ID: r.ID, Platform: model.Platform(r.Platform), Type: model.SourceType(r.Type), ExternalID: r.ExternalID,
		Name: r.Name, Note: r.Note, OwnerID: r.OwnerID, OwnerName: r.OwnerName, Enabled: r.Enabled != 0,
		ZSXQTopicMode: model.ZSXQTopicMode(r.ZSXQTopicMode), ZSXQAuthors: authors,
		BaselineState: model.BaselineState(r.BaselineState), BackfillCursor: r.BackfillCursor, HighWatermark: r.HighWatermark,
		BackfillDone: r.BackfillDone, BackfillTotal: r.BackfillTotal, LastPollAt: pointerTime(r.LastPollAt),
		LastSuccessAt: pointerTime(r.LastSuccessAt), LastCommentAt: pointerTime(r.LastCommentAt), SyncLagSec: r.SyncLagSec,
		LastError: r.LastError, ConsecutiveFails: r.ConsecutiveFails,
	}
}

type contentRow struct {
	ID                string `gorm:"column:id;primaryKey"`
	Platform          string `gorm:"column:platform"`
	SourceID          string `gorm:"column:source_id"`
	ExternalID        string `gorm:"column:external_id"`
	AuthorID          string `gorm:"column:author_id"`
	AuthorName        string `gorm:"column:author_name"`
	UpstreamType      string `gorm:"column:upstream_type"`
	Type              string `gorm:"column:type"`
	Title             string `gorm:"column:title"`
	BodyText          string `gorm:"column:body_text"`
	SafeHTML          string `gorm:"column:safe_html"`
	URL               string `gorm:"column:url"`
	PublishedAt       int64  `gorm:"column:published_at"`
	UpstreamUpdatedAt *int64 `gorm:"column:upstream_updated_at"`
	FirstSeenAt       int64  `gorm:"column:first_seen_at"`
	LastSyncedAt      int64  `gorm:"column:last_synced_at"`
	DeletedAt         *int64 `gorm:"column:deleted_at"`
	StatsJSON         string `gorm:"column:stats_json"`
	TreeIncomplete    int    `gorm:"column:tree_incomplete"`
	Baseline          int    `gorm:"column:baseline"`
	SearchText        string `gorm:"column:search_text"`
	RawJSON           string `gorm:"column:raw_json"`
}

func (contentRow) TableName() string { return "contents" }

type attachmentRow struct {
	ID            string `gorm:"column:id;primaryKey"`
	ContentID     string `gorm:"column:content_id"`
	ExternalID    string `gorm:"column:external_id"`
	Type          string `gorm:"column:type"`
	FileName      string `gorm:"column:file_name"`
	MIME          string `gorm:"column:mime"`
	Size          int64  `gorm:"column:size"`
	Width         int    `gorm:"column:width"`
	Height        int    `gorm:"column:height"`
	DurationSec   int64  `gorm:"column:duration_sec"`
	RemoteURL     string `gorm:"column:remote_url"`
	RemoteHost    string `gorm:"column:remote_host"`
	LocalPath     string `gorm:"column:local_path"`
	LocalizeError string `gorm:"column:localize_error"`
}

func (attachmentRow) TableName() string { return "attachments" }

type commentNodeRow struct {
	ID                string `gorm:"column:id;primaryKey"`
	Platform          string `gorm:"column:platform"`
	ContentID         string `gorm:"column:content_id"`
	ExternalID        string `gorm:"column:external_id"`
	RootID            string `gorm:"column:root_id"`
	ParentID          string `gorm:"column:parent_id"`
	AuthorID          string `gorm:"column:author_id"`
	AuthorName        string `gorm:"column:author_name"`
	AuthorRole        string `gorm:"column:author_role"`
	BodyText          string `gorm:"column:body_text"`
	MediaJSON         string `gorm:"column:media_json"`
	PublishedAt       int64  `gorm:"column:published_at"`
	UpstreamUpdatedAt *int64 `gorm:"column:upstream_updated_at"`
	FirstSeenAt       int64  `gorm:"column:first_seen_at"`
	DeletedAt         *int64 `gorm:"column:deleted_at"`
	Baseline          int    `gorm:"column:baseline"`
}

func (commentNodeRow) TableName() string { return "comment_nodes" }

type seenItemRow struct {
	Platform    string `gorm:"column:platform;primaryKey"`
	SourceID    string `gorm:"column:source_id;primaryKey"`
	EntityType  string `gorm:"column:entity_type;primaryKey"`
	EntityID    string `gorm:"column:entity_id;primaryKey"`
	FirstSeenAt int64  `gorm:"column:first_seen_at"`
}

func (seenItemRow) TableName() string { return "seen_items" }

type syncTargetRow struct {
	Platform      string `gorm:"column:platform;primaryKey"`
	ContentID     string `gorm:"column:content_id;primaryKey"`
	SourceID      string `gorm:"column:source_id"`
	CommentType   int    `gorm:"column:comment_type"`
	CommentOID    string `gorm:"column:comment_oid"`
	Title         string `gorm:"column:title"`
	URL           string `gorm:"column:url"`
	PublishedAt   int64  `gorm:"column:published_at"`
	CommentCount  int64  `gorm:"column:comment_count"`
	Closed        int    `gorm:"column:closed"`
	BaselineReady int    `gorm:"column:baseline_ready"`
	LastSyncedAt  *int64 `gorm:"column:last_synced_at"`
	LastError     string `gorm:"column:last_error"`
}

func (syncTargetRow) TableName() string { return "sync_targets" }

func syncTargetFromModel(target model.CommentTarget) syncTargetRow {
	contentID := model.ContentID(model.PlatformBilibili, target.DynamicID)
	return syncTargetRow{Platform: string(model.PlatformBilibili), ContentID: contentID,
		SourceID: model.SourceID(model.PlatformBilibili, target.UID), CommentType: target.CommentType,
		CommentOID: target.CommentOID, Title: target.Title, URL: target.URL, PublishedAt: target.PublishedAt.Unix(),
		CommentCount: target.CommentCount, Closed: boolToInt(target.Closed), BaselineReady: boolToInt(target.BaselineReady),
		LastSyncedAt: unixPointer(target.LastPollAt), LastError: target.LastError}
}

func (row syncTargetRow) toCommentTarget() model.CommentTarget {
	return model.CommentTarget{UID: strings.TrimPrefix(row.SourceID, "bilibili:up:"), DynamicID: strings.TrimPrefix(row.ContentID, "bilibili:content:"),
		Title: row.Title, URL: row.URL, CommentType: row.CommentType, CommentOID: row.CommentOID,
		PublishedAt: time.Unix(row.PublishedAt, 0), CommentCount: row.CommentCount, Closed: row.Closed != 0,
		BaselineReady: row.BaselineReady != 0, LastPollAt: pointerTime(row.LastSyncedAt), LastError: row.LastError}
}

// ArchiveContent stores a current upstream snapshot and attachments. FirstSeenAt
// is immutable across edits, deletion and restoration.
func (s *Store) ArchiveContent(content model.Content, attachments []model.Attachment) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return archiveContentTx(tx, content, attachments)
	})
}

// ArchiveContentAndEnqueue commits archive/seen/outbox as one transaction.
// notify must be false for initial baselines and historical backfill.
func (s *Store) ArchiveContentAndEnqueue(content model.Content, attachments []model.Attachment, _ []string, notify bool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&contentRow{}).Where("id = ?", content.ID).Count(&existing).Error; err != nil {
			return err
		}
		if err := archiveContentTx(tx, content, attachments); err != nil {
			return err
		}
		if existing != 0 || !notify {
			return nil
		}
		channelIDs, err := enabledChannelIDsTx(tx)
		if err != nil {
			return err
		}
		now := time.Now()
		snapshot, err := contentDeliverySnapshotTx(tx, content, attachments)
		if err != nil {
			return err
		}
		for _, channelID := range channelIDs {
			key := "content:" + content.ID
			delivery := model.Delivery{ID: stableHash(key, channelID), Kind: model.DeliveryKindContent, Content: &snapshot,
				ChannelID: channelID, State: model.DeliveryPending, NextAt: now, CreatedAt: now,
				OriginTraceparent: originTraceparent(tx.Statement.Context)}
			if err := putDeliveryTx(tx, delivery); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) PutCommentSyncState(platform model.Platform, contentID string, baselineReady bool, syncedAt time.Time, lastError string) error {
	if err := platform.Validate(); err != nil {
		return err
	}
	row := map[string]any{"platform": string(platform), "content_id": contentID, "baseline_ready": boolToInt(baselineReady), "last_error": lastError}
	if !syncedAt.IsZero() {
		row["last_synced_at"] = syncedAt.Unix()
	}
	return s.db.Table("sync_targets").Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "platform"}, {Name: "content_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"baseline_ready": gorm.Expr("MAX(sync_targets.baseline_ready, excluded.baseline_ready)"),
			"last_synced_at": gorm.Expr("excluded.last_synced_at"),
			"last_error":     gorm.Expr("excluded.last_error"),
		}),
	}).Create(row).Error
}

func (s *Store) CommentSyncState(platform model.Platform, contentID string) (bool, error) {
	var row struct {
		BaselineReady int `gorm:"column:baseline_ready"`
	}
	err := s.db.Table("sync_targets").Select("baseline_ready").Where("platform = ? AND content_id = ?", platform, contentID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.BaselineReady != 0, err
}

func archiveContentTx(tx *gorm.DB, content model.Content, attachments []model.Attachment) error {
	if err := content.Validate(); err != nil {
		return err
	}
	now := time.Now()
	if content.FirstSeenAt.IsZero() {
		content.FirstSeenAt = now
	}
	if content.LastSyncedAt.IsZero() {
		content.LastSyncedAt = now
	}
	stats, err := json.Marshal(content.Stats)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	row := contentRow{
		ID: content.ID, Platform: string(content.Platform), SourceID: content.SourceID, ExternalID: content.ExternalID,
		AuthorID: content.AuthorID, AuthorName: content.AuthorName, UpstreamType: content.UpstreamType, Type: string(content.Type),
		Title: content.Title, BodyText: content.Text, SafeHTML: content.SafeHTML, URL: content.URL, PublishedAt: content.PublishedAt.Unix(),
		UpstreamUpdatedAt: unixPointer(content.UpdatedAt), FirstSeenAt: content.FirstSeenAt.Unix(), LastSyncedAt: content.LastSyncedAt.Unix(),
		DeletedAt: unixPointer(content.DeletedAt), StatsJSON: string(stats), TreeIncomplete: boolToInt(content.TreeIncomplete), Baseline: boolToInt(content.Baseline),
		SearchText: foldSearch(strings.Join([]string{content.AuthorName, content.Title, content.Text}, " ")), RawJSON: string(raw),
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"author_id", "author_name", "upstream_type", "type", "title", "body_text", "safe_html", "url", "published_at", "upstream_updated_at", "last_synced_at", "deleted_at", "stats_json", "tree_incomplete", "search_text", "raw_json"}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("archiving content %s: %w", content.ID, err)
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seenItemRow{
		Platform: string(content.Platform), SourceID: content.SourceID, EntityType: "content", EntityID: content.ID, FirstSeenAt: content.FirstSeenAt.Unix(),
	}).Error; err != nil {
		return err
	}
	for _, attachment := range attachments {
		if attachment.ContentID != content.ID || attachment.ID == "" || attachment.ExternalID == "" {
			return errors.New("attachment identity does not match content")
		}
		remoteHost := attachment.RemoteHost
		if remoteHost == "" && attachment.RemoteURL != "" {
			if parsed, parseErr := url.Parse(attachment.RemoteURL); parseErr == nil {
				remoteHost = parsed.Hostname()
			}
		}
		a := attachmentRow{ID: attachment.ID, ContentID: attachment.ContentID, ExternalID: attachment.ExternalID, Type: string(attachment.Type), FileName: attachment.FileName,
			MIME: attachment.MIME, Size: attachment.Size, Width: attachment.Width, Height: attachment.Height, DurationSec: attachment.DurationSec,
			RemoteURL: attachment.RemoteURL, RemoteHost: remoteHost, LocalPath: attachment.LocalPath, LocalizeError: attachment.LocalizeError}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{
			"type", "file_name", "mime", "size", "width", "height", "duration_sec", "remote_url", "remote_host", "local_path", "localize_error",
		})}).Create(&a).Error; err != nil {
			return err
		}
	}
	return nil
}

type PlatformContentQuery struct {
	Platform model.Platform
	SourceID string
	Keyword  string
	From     time.Time
	To       time.Time
	AfterAt  time.Time
	AfterID  string
	Limit    int
}

func (s *Store) QueryContents(query PlatformContentQuery) ([]model.Content, error) {
	limit, _ := normalizePage(query.Limit, 0)
	db := s.db.Order("published_at DESC, id DESC").Limit(limit)
	if query.Platform != "" {
		if err := query.Platform.Validate(); err != nil {
			return nil, err
		}
		db = db.Where("platform = ?", query.Platform)
	}
	if query.SourceID != "" {
		db = db.Where("source_id = ?", query.SourceID)
	}
	if keyword := foldSearch(query.Keyword); keyword != "" {
		db = db.Where("search_text LIKE ?", "%"+keyword+"%")
	}
	if !query.From.IsZero() {
		db = db.Where("published_at >= ?", query.From.Unix())
	}
	if !query.To.IsZero() {
		db = db.Where("published_at < ?", query.To.Unix())
	}
	if !query.AfterAt.IsZero() && query.AfterID != "" {
		db = db.Where("published_at < ? OR (published_at = ? AND id < ?)", query.AfterAt.Unix(), query.AfterAt.Unix(), query.AfterID)
	}
	var rows []contentRow
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]model.Content, 0, len(rows))
	for _, row := range rows {
		items = append(items, contentModel(row))
	}
	return items, nil
}

func (s *Store) Content(id string) (model.Content, []model.Attachment, error) {
	var row contentRow
	err := s.db.Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Content{}, nil, ErrNotFound
	}
	if err != nil {
		return model.Content{}, nil, err
	}
	var attachmentRows []attachmentRow
	if err := s.db.Where("content_id = ?", id).Order("id").Find(&attachmentRows).Error; err != nil {
		return model.Content{}, nil, err
	}
	attachments := make([]model.Attachment, 0, len(attachmentRows))
	for _, item := range attachmentRows {
		attachments = append(attachments, item.toModel(false))
	}
	return contentModel(row), attachments, nil
}

func (s *Store) Attachment(contentID, attachmentID string, includeRemote bool) (model.Attachment, error) {
	var row attachmentRow
	err := s.db.Where("content_id = ? AND id = ?", contentID, attachmentID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Attachment{}, ErrNotFound
	}
	return row.toModel(includeRemote), err
}

// MarkContentDeleted records a remote tombstone without changing first-seen
// state. A later ArchiveContent call clears the tombstone and represents a
// restoration without creating another notification.
func (s *Store) MarkContentDeleted(contentID string, deletedAt time.Time) error {
	if deletedAt.IsZero() {
		deletedAt = time.Now()
	}
	result := s.db.Model(&contentRow{}).Where("id = ? AND deleted_at IS NULL", contentID).Update("deleted_at", deletedAt.Unix())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := s.db.Model(&contentRow{}).Where("id = ?", contentID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
	}
	return nil
}

func contentModel(row contentRow) model.Content {
	content := model.Content{
		ID: row.ID, Platform: model.Platform(row.Platform), SourceID: row.SourceID, ExternalID: row.ExternalID,
		AuthorID: row.AuthorID, AuthorName: row.AuthorName, UpstreamType: row.UpstreamType, Type: model.ContentType(row.Type),
		Title: row.Title, Text: row.BodyText, SafeHTML: row.SafeHTML, URL: row.URL, PublishedAt: time.Unix(row.PublishedAt, 0),
		UpdatedAt: pointerTime(row.UpstreamUpdatedAt), FirstSeenAt: time.Unix(row.FirstSeenAt, 0), LastSyncedAt: time.Unix(row.LastSyncedAt, 0),
		DeletedAt: pointerTime(row.DeletedAt), TreeIncomplete: row.TreeIncomplete != 0, Baseline: row.Baseline != 0,
	}
	_ = json.Unmarshal([]byte(row.StatsJSON), &content.Stats)
	return content
}

func (row attachmentRow) toModel(includeRemote bool) model.Attachment {
	attachment := model.Attachment{ID: row.ID, ContentID: row.ContentID, ExternalID: row.ExternalID, Type: model.AttachmentType(row.Type), FileName: row.FileName,
		MIME: row.MIME, Size: row.Size, Width: row.Width, Height: row.Height, DurationSec: row.DurationSec, RemoteHost: row.RemoteHost,
		LocalPath: row.LocalPath, LocalizeError: row.LocalizeError}
	if includeRemote {
		attachment.RemoteURL = row.RemoteURL
	}
	return attachment
}

// SyncCommentTree archives every comment first, derives target-author triggers
// only from never-before-seen IDs, and creates one digest per channel. A complete
// snapshot is required before missing rows are marked deleted.
func (s *Store) SyncCommentTree(content model.Content, nodes []model.CommentNode, complete, baseline bool, batchID string, target *model.CommentTarget) ([]model.CommentDigest, error) {
	meta, ok := platformcontract.BuiltinMeta(content.Platform)
	if !ok {
		return nil, fmt.Errorf("platform metadata is not registered for %q", content.Platform)
	}
	return s.SyncCommentTreeWithMeta(meta, content, nodes, complete, baseline, batchID, target)
}

func (s *Store) SyncCommentTreeWithMeta(meta platformcontract.PlatformMeta, content model.Content, nodes []model.CommentNode, complete, baseline bool, batchID string, target *model.CommentTarget) ([]model.CommentDigest, error) {
	if err := meta.Validate(); err != nil || meta.Platform != content.Platform {
		return nil, errors.New("comment platform metadata does not match content")
	}
	var digests []model.CommentDigest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := archiveContentTx(tx, content, nil); err != nil {
			return err
		}
		now := time.Now()
		incoming := make(map[string]bool, len(nodes))
		incomingNodes := make([]model.CommentNode, 0, len(nodes))
		newTriggerIDs := make([]string, 0)
		for _, node := range nodes {
			if node.ID == "" {
				node.ID = model.CommentID(content.Platform, node.RPID)
			}
			if node.Platform == "" {
				node.Platform = content.Platform
			}
			if node.ContentID == "" {
				node.ContentID = content.ID
			}
			if node.ID == "" || node.ContentID != content.ID || node.Platform != content.Platform {
				return errors.New("comment identity does not match content")
			}
			externalID := node.RPID
			if externalID == "" {
				externalID = strings.TrimPrefix(node.ID, string(content.Platform)+":comment:")
			}
			incoming[node.ID] = true
			var existing commentNodeRow
			lookupErr := tx.Where("id = ?", node.ID).Take(&existing).Error
			isNew := errors.Is(lookupErr, gorm.ErrRecordNotFound)
			if lookupErr != nil && !isNew {
				return lookupErr
			}
			firstSeen := node.FirstSeenAt
			if firstSeen.IsZero() {
				firstSeen = now
			}
			mediaJSON, err := json.Marshal(node.Media)
			if err != nil {
				return err
			}
			published := node.Time
			if published.IsZero() {
				published = now
			}
			parentID := node.ParentID
			if parentID == "" && node.Parent != "" {
				parentID = model.CommentID(content.Platform, node.Parent)
			}
			rootID := node.RootID
			if rootID == "" && parentID == "" {
				rootID = node.ID
			}
			node.RootID, node.ParentID = rootID, parentID
			incomingNodes = append(incomingNodes, node)
			row := commentNodeRow{ID: node.ID, Platform: string(content.Platform), ContentID: content.ID, ExternalID: externalID,
				RootID: rootID, ParentID: parentID, AuthorID: firstNonEmpty(node.AuthorID, node.Mid), AuthorName: node.Name,
				AuthorRole: string(node.Role), BodyText: node.Message, MediaJSON: string(mediaJSON), PublishedAt: published.Unix(),
				UpstreamUpdatedAt: unixPointer(node.UpdatedAt), FirstSeenAt: firstSeen.Unix(), DeletedAt: nil, Baseline: boolToInt(baseline)}
			if row.AuthorRole == "" {
				row.AuthorRole = string(model.RoleMember)
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{
				"root_id", "parent_id", "author_id", "author_name", "author_role", "body_text", "media_json", "published_at", "upstream_updated_at", "deleted_at",
			})}).Create(&row).Error; err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seenItemRow{Platform: string(content.Platform), SourceID: content.SourceID,
				EntityType: "comment", EntityID: node.ID, FirstSeenAt: firstSeen.Unix()}).Error; err != nil {
				return err
			}
			isTarget := meta.Triggers(node.Role)
			if isNew && !baseline && isTarget {
				newTriggerIDs = append(newTriggerIDs, node.ID)
			}
		}
		_, incomingMalformed := model.BuildCommentTree(incomingNodes)
		complete = complete && !incomingMalformed
		if complete {
			var existingIDs []string
			if err := tx.Model(&commentNodeRow{}).Where("content_id = ? AND deleted_at IS NULL", content.ID).Pluck("id", &existingIDs).Error; err != nil {
				return err
			}
			for _, id := range existingIDs {
				if !incoming[id] {
					if err := tx.Model(&commentNodeRow{}).Where("id = ?", id).Update("deleted_at", now.Unix()).Error; err != nil {
						return err
					}
				}
			}
		}
		var currentRows []commentNodeRow
		if err := tx.Where("content_id = ?", content.ID).Order("published_at, id").Find(&currentRows).Error; err != nil {
			return err
		}
		current := commentModels(currentRows)
		_, malformed := model.BuildCommentTree(current)
		malformed = malformed || incomingMalformed
		if err := tx.Model(&contentRow{}).Where("id = ?", content.ID).Update("tree_incomplete", boolToInt(malformed || !complete)).Error; err != nil {
			return err
		}
		if err := updateCommentSyncTargetTx(tx, content, complete, now, target); err != nil {
			return err
		}
		if len(newTriggerIDs) == 0 {
			return nil
		}
		slices.Sort(newTriggerIDs)
		digest := model.CommentDigest{Platform: content.Platform, SourceID: content.SourceID, ContentID: content.ID, BatchID: batchID, Title: content.Title, ContentURL: content.URL}
		byID := make(map[string]model.CommentNode, len(current))
		for _, node := range current {
			byID[node.ID] = node
		}
		for _, id := range newTriggerIDs {
			node := byID[id]
			node.IsTrigger = true
			digest.Triggers = append(digest.Triggers, node)
			path, pathComplete := model.CommentAncestorPath(current, id)
			if !pathComplete {
				malformed = true
			}
			digest.Paths = append(digest.Paths, model.CommentPath{TriggerID: id, Nodes: path})
		}
		digest.Incomplete = malformed || !complete
		key := digest.DigestKey()
		if batchID != "" {
			key += ":" + batchID
		}
		channelIDs, err := enabledChannelIDsTx(tx)
		if err != nil {
			return err
		}
		for _, channelID := range channelIDs {
			id := stableHash("comment_digest", channelID, key)
			note, err := commentDigestNotificationTx(tx, digest, key, now)
			if err != nil {
				return err
			}
			delivery := model.Delivery{ID: id, Kind: model.DeliveryKindComment, Comment: &note,
				ChannelID: channelID, State: model.DeliveryPending, NextAt: now, CreatedAt: now,
				OriginTraceparent: originTraceparent(tx.Statement.Context)}
			if err := putDeliveryTx(tx, delivery); err != nil {
				return err
			}
		}
		digests = append(digests, digest)
		return nil
	})
	return digests, err
}

func updateCommentSyncTargetTx(tx *gorm.DB, content model.Content, complete bool, syncedAt time.Time, target *model.CommentTarget) error {
	row := syncTargetRow{
		Platform:      string(content.Platform),
		ContentID:     content.ID,
		SourceID:      content.SourceID,
		BaselineReady: boolToInt(complete),
		LastSyncedAt:  unixPointer(syncedAt),
	}
	updates := []string{"source_id", "baseline_ready", "last_synced_at", "last_error"}
	if target != nil {
		if content.Platform != model.PlatformBilibili || target.UID == "" || model.SourceID(model.PlatformBilibili, target.UID) != content.SourceID {
			return errors.New("Bilibili comment target does not match content")
		}
		updated := *target
		updated.BaselineReady = updated.BaselineReady || complete
		updated.LastPollAt = syncedAt
		updated.LastError = ""
		row = syncTargetFromModel(updated)
		updates = []string{"source_id", "comment_type", "comment_oid", "title", "url", "published_at", "comment_count", "closed", "baseline_ready", "last_synced_at", "last_error"}
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "platform"}, {Name: "content_id"}},
		DoUpdates: clause.AssignmentColumns(updates),
	}).Create(&row).Error
}

func (s *Store) CommentTree(contentID string) ([]model.CommentNode, bool, error) {
	var rows []commentNodeRow
	if err := s.db.Where("content_id = ?", contentID).Order("published_at, id").Find(&rows).Error; err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		var count int64
		if err := s.db.Model(&contentRow{}).Where("id = ?", contentID).Count(&count).Error; err != nil {
			return nil, false, err
		}
		if count == 0 {
			return nil, false, ErrNotFound
		}
	}
	tree, incomplete := model.BuildCommentTree(commentModels(rows))
	if tree == nil {
		tree = make([]model.CommentNode, 0)
	}
	var content contentRow
	if err := s.db.Select("tree_incomplete").Where("id = ?", contentID).Take(&content).Error; err == nil {
		incomplete = incomplete || content.TreeIncomplete != 0
	}
	return tree, incomplete, nil
}

func enabledChannelIDsTx(tx *gorm.DB) ([]string, error) {
	var ids []string
	return ids, tx.Model(&channelRow{}).Where("enabled = ?", 1).Order("id").Pluck("id", &ids).Error
}

func contentDeliverySnapshotTx(tx *gorm.DB, content model.Content, attachments []model.Attachment) (model.ContentSnapshot, error) {
	var source sourceRow
	if err := tx.Select("name").Where("id = ?", content.SourceID).Take(&source).Error; err != nil {
		return model.ContentSnapshot{}, err
	}
	snapshot := model.ContentSnapshot{Platform: content.Platform, SourceID: content.SourceID, SourceName: source.Name,
		ContentID: content.ID, ExternalID: content.ExternalID, AuthorID: content.AuthorID, AuthorName: content.AuthorName,
		Type: content.Type, UpstreamType: content.UpstreamType, PublishedAt: content.PublishedAt, Text: content.Text,
		Description: content.Text, URL: content.URL, Title: content.Title, Stats: maps.Clone(content.Stats)}
	fileIndex := 0
	for _, attachment := range attachments {
		if attachment.Type == model.AttachmentImage {
			snapshot.Media = append(snapshot.Media, model.SnapshotMedia{ID: attachment.ID, Type: attachment.Type, Name: attachment.FileName,
				URL: attachment.RemoteURL, Width: attachment.Width, Height: attachment.Height, LocalPath: attachment.LocalPath, MIME: attachment.MIME, Size: attachment.Size})
			continue
		}
		label := attachment.FileName
		if label == "" {
			label = string(attachment.Type)
		}
		if attachment.Size > 0 {
			label += fmt.Sprintf(" (%s)", humanBytes(attachment.Size))
		}
		snapshot.Links = append(snapshot.Links, model.SnapshotLink{Text: label, URL: content.URL})
		if content.Platform == model.PlatformZSXQ && attachment.Type != model.AttachmentImage && attachment.Type != model.AttachmentLink {
			fileIndex++
			name := deliveryFileName(attachment, fileIndex)
			snapshot.Files = append(snapshot.Files, model.DeliveryFile{ID: attachment.ID, Name: name, MIME: attachment.MIME,
				Size: attachment.Size, LocalPath: attachment.LocalPath, LocalizeError: attachment.LocalizeError})
		}
	}
	return snapshot, nil
}

func deliveryFileName(attachment model.Attachment, index int) string {
	if name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(attachment.FileName, `\`, "/"))); name != "" && name != "." {
		return name
	}
	ext := filepath.Ext(filepath.Base(attachment.LocalPath))
	if ext == "" {
		switch strings.ToLower(attachment.MIME) {
		case "audio/mpeg":
			ext = ".mp3"
		case "video/mp4":
			ext = ".mp4"
		case "application/pdf":
			ext = ".pdf"
		}
	}
	return fmt.Sprintf("附件-%d%s", index, ext)
}

func commentDigestNotificationTx(tx *gorm.DB, digest model.CommentDigest, key string, now time.Time) (model.CommentNotification, error) {
	thread, seen, published := make([]model.CommentNode, 0), make(map[string]bool), time.Time{}
	for _, path := range digest.Paths {
		for _, node := range path.Nodes {
			if !seen[node.ID] {
				node.IsTrigger = node.ID == path.TriggerID
				thread, seen[node.ID] = append(thread, node), true
			}
			if node.Time.After(published) {
				published = node.Time
			}
		}
	}
	if published.IsZero() {
		published = now
	}
	var source sourceRow
	if err := tx.Select("name").Where("id = ?", digest.SourceID).Take(&source).Error; err != nil {
		return model.CommentNotification{}, fmt.Errorf("loading comment digest source %s: %w", digest.SourceID, err)
	}
	var content contentRow
	if err := tx.Select("upstream_type").Where("id = ?", digest.ContentID).Take(&content).Error; err != nil {
		return model.CommentNotification{}, fmt.Errorf("loading comment digest content %s: %w", digest.ContentID, err)
	}
	authorID, authorName, authorRole := digest.SourceID, source.Name, model.RoleMember
	if len(digest.Triggers) != 0 {
		authorID = firstNonEmpty(digest.Triggers[0].AuthorID, digest.Triggers[0].Mid)
		authorName = digest.Triggers[0].Name
		authorRole = digest.Triggers[0].Role
	}
	note := model.CommentNotification{RPID: key, Platform: digest.Platform, SourceID: digest.SourceID, SourceName: source.Name,
		AuthorID: authorID, AuthorName: authorName, AuthorRole: authorRole, ContentType: content.UpstreamType, ContentID: digest.ContentID,
		ContentTitle: digest.Title, ContentURL: digest.ContentURL, PublishedAt: published, Incomplete: digest.Incomplete, Thread: thread}
	if len(digest.Triggers) == 1 {
		note.RPID = digest.Triggers[0].RPID
	}
	return note, nil
}

func humanBytes(size int64) string {
	const unit = int64(1024)
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value, index := float64(size)/float64(unit), 0
	for value >= float64(unit) && index < len(units)-1 {
		value /= float64(unit)
		index++
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}

func commentModels(rows []commentNodeRow) []model.CommentNode {
	items := make([]model.CommentNode, 0, len(rows))
	for _, row := range rows {
		var media []model.Attachment
		_ = json.Unmarshal([]byte(row.MediaJSON), &media)
		items = append(items, model.CommentNode{ID: row.ID, Platform: model.Platform(row.Platform), ContentID: row.ContentID, RootID: row.RootID, ParentID: row.ParentID,
			RPID: row.ExternalID, AuthorID: row.AuthorID, Mid: row.AuthorID, Name: row.AuthorName, Role: model.AuthorRole(row.AuthorRole), Message: row.BodyText,
			Media: media, Time: time.Unix(row.PublishedAt, 0), UpdatedAt: pointerTime(row.UpstreamUpdatedAt), FirstSeenAt: time.Unix(row.FirstSeenAt, 0), DeletedAt: pointerTime(row.DeletedAt),
			IsUP: row.AuthorRole == string(model.RoleUP)})
	}
	return items
}

func stableHash(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:])
}

func unixPointer(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	unix := value.Unix()
	return &unix
}

func pointerTime(value *int64) time.Time {
	if value == nil {
		return time.Time{}
	}
	return time.Unix(*value, 0)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
