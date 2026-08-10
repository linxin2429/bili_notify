package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
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
		if row.SealedAAD == tableAuthSession && platform == model.PlatformBilibili {
			var legacy model.BiliSession
			if err := openJSON(s.vault, tableAuthSession, authSessionID, row.SealedSession, &legacy); err != nil {
				return model.PlatformAccount{}, err
			}
			account.Session = legacy.Cookies
			if account.ExternalID == "" {
				account.ExternalID, account.DisplayName = legacy.AccountUID, legacy.AccountName
			}
		} else if err := openJSON(s.vault, tablePlatformAccounts, string(platform), row.SealedSession, &account.Session); err != nil {
			return model.PlatformAccount{}, err
		}
	}
	return account, nil
}

func (s *Store) ListPlatformAccounts() ([]model.PlatformAccount, error) {
	accounts := make([]model.PlatformAccount, 0, 2)
	for _, platform := range []model.Platform{model.PlatformBilibili, model.PlatformZSXQ} {
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
	ID               string `gorm:"column:id;primaryKey"`
	Platform         string `gorm:"column:platform"`
	Type             string `gorm:"column:type"`
	ExternalID       string `gorm:"column:external_id"`
	Name             string `gorm:"column:name"`
	Note             string `gorm:"column:note"`
	OwnerID          string `gorm:"column:owner_id"`
	OwnerName        string `gorm:"column:owner_name"`
	Enabled          int    `gorm:"column:enabled"`
	BaselineState    string `gorm:"column:baseline_state"`
	BackfillCursor   string `gorm:"column:backfill_cursor"`
	HighWatermark    string `gorm:"column:high_watermark"`
	BackfillDone     int64  `gorm:"column:backfill_done"`
	BackfillTotal    int64  `gorm:"column:backfill_total"`
	LastPollAt       *int64 `gorm:"column:last_poll_at"`
	LastSuccessAt    *int64 `gorm:"column:last_success_at"`
	LastCommentAt    *int64 `gorm:"column:last_comment_at"`
	SyncLagSec       int64  `gorm:"column:sync_lag_sec"`
	LastError        string `gorm:"column:last_error"`
	ConsecutiveFails int    `gorm:"column:consecutive_fails"`
}

func (sourceRow) TableName() string { return "sources" }

func (s *Store) PutSource(source model.Source) error {
	if source.BaselineState == "" {
		source.BaselineState = model.BaselinePending
	}
	if err := source.Validate(); err != nil {
		return err
	}
	return s.db.Save(sourceFromModel(source)).Error
}

// MergeVisibleSources inserts/updates upstream-owned metadata without changing
// the administrator's Enabled, note, or baseline/backfill choices.
func (s *Store) MergeVisibleSources(platform model.Platform, sources []model.Source) error {
	if err := platform.Validate(); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, source := range sources {
			if source.Platform != platform {
				return errors.New("visible source platform mismatch")
			}
			if source.BaselineState == "" {
				source.BaselineState = model.BaselinePending
			}
			if err := source.Validate(); err != nil {
				return err
			}
			row := sourceFromModel(source)
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"name", "owner_id", "owner_name"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
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
	return s.db.Transaction(func(tx *gorm.DB) error {
		var deliveries []deliveryRow
		if err := tx.Find(&deliveries).Error; err != nil {
			return err
		}
		for _, row := range deliveries {
			delivery, err := decodeDelivery(row)
			if err != nil {
				return err
			}
			belongsToSource := delivery.EffectiveKind() == model.DeliveryKindDynamic && delivery.Dynamic.UID == id
			if delivery.EffectiveKind() == model.DeliveryKindComment && delivery.Comment != nil {
				belongsToSource = delivery.Comment.UPUID == id
			}
			if belongsToSource {
				if err := tx.Where("id = ?", row.ID).Delete(&deliveryRow{}).Error; err != nil {
					return err
				}
			}
		}
		result := tx.Where("id = ?", id).Delete(&sourceRow{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("source_id = ?", id).Delete(&seenItemRow{}).Error; err != nil {
			return err
		}
		return tx.Where("source_id = ? AND state IN ?", id, []string{"pending", "blocked"}).Delete(&outboxRow{}).Error
	})
}

func sourceFromModel(source model.Source) sourceRow {
	return sourceRow{
		ID: source.ID, Platform: string(source.Platform), Type: string(source.Type), ExternalID: source.ExternalID,
		Name: source.Name, Note: source.Note, OwnerID: source.OwnerID, OwnerName: source.OwnerName,
		Enabled: boolToInt(source.Enabled), BaselineState: string(source.BaselineState), BackfillCursor: source.BackfillCursor,
		HighWatermark: source.HighWatermark, BackfillDone: source.BackfillDone, BackfillTotal: source.BackfillTotal,
		LastPollAt: unixPointer(source.LastPollAt), LastSuccessAt: unixPointer(source.LastSuccessAt), LastCommentAt: unixPointer(source.LastCommentAt),
		SyncLagSec: source.SyncLagSec, LastError: source.LastError, ConsecutiveFails: source.ConsecutiveFails,
	}
}

func (r sourceRow) toModel() model.Source {
	return model.Source{
		ID: r.ID, Platform: model.Platform(r.Platform), Type: model.SourceType(r.Type), ExternalID: r.ExternalID,
		Name: r.Name, Note: r.Note, OwnerID: r.OwnerID, OwnerName: r.OwnerName, Enabled: r.Enabled != 0,
		BaselineState: model.BaselineState(r.BaselineState), BackfillCursor: r.BackfillCursor, HighWatermark: r.HighWatermark,
		BackfillDone: r.BackfillDone, BackfillTotal: r.BackfillTotal, LastPollAt: pointerTime(r.LastPollAt),
		LastSuccessAt: pointerTime(r.LastSuccessAt), LastCommentAt: pointerTime(r.LastCommentAt), SyncLagSec: r.SyncLagSec,
		LastError: r.LastError, ConsecutiveFails: r.ConsecutiveFails,
	}
}

type contentRowV3 struct {
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

func (contentRowV3) TableName() string { return "contents" }

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

type outboxRow struct {
	ID                string `gorm:"column:id;primaryKey"`
	Kind              string `gorm:"column:kind"`
	Platform          string `gorm:"column:platform"`
	SourceID          string `gorm:"column:source_id"`
	ContentID         string `gorm:"column:content_id"`
	ChannelID         string `gorm:"column:channel_id"`
	IdempotencyKey    string `gorm:"column:idempotency_key"`
	State             string `gorm:"column:state"`
	Attempts          int    `gorm:"column:attempts"`
	NextAt            int64  `gorm:"column:next_at"`
	LastError         string `gorm:"column:last_error"`
	CreatedAt         int64  `gorm:"column:created_at"`
	PayloadJSON       string `gorm:"column:payload_json"`
	ProgressJSON      string `gorm:"column:progress_json"`
	OriginTraceparent string `gorm:"column:origin_traceparent"`
}

func (outboxRow) TableName() string { return "outbox" }

// ArchiveContent stores a current upstream snapshot and attachments. FirstSeenAt
// is immutable across edits, deletion and restoration.
func (s *Store) ArchiveContent(content model.Content, attachments []model.Attachment) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return archiveContentTx(tx, content, attachments)
	})
}

// ArchiveContentAndEnqueue commits archive/seen/outbox as one transaction.
// notify must be false for initial baselines and historical backfill.
func (s *Store) ArchiveContentAndEnqueue(content model.Content, attachments []model.Attachment, channelIDs []string, notify bool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&contentRowV3{}).Where("id = ?", content.ID).Count(&existing).Error; err != nil {
			return err
		}
		if err := archiveContentTx(tx, content, attachments); err != nil {
			return err
		}
		if existing != 0 || !notify {
			return nil
		}
		payload, err := json.Marshal(content)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		for _, channelID := range channelIDs {
			key := "content:" + content.ID
			row := outboxRow{ID: stableHash(key, channelID), Kind: "content", Platform: string(content.Platform), SourceID: content.SourceID,
				ContentID: content.ID, ChannelID: channelID, IdempotencyKey: key, State: "pending", NextAt: now, CreatedAt: now,
				PayloadJSON: string(payload), ProgressJSON: "{}"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
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
		Columns:   []clause.Column{{Name: "platform"}, {Name: "content_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"baseline_ready", "last_synced_at", "last_error"}),
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
	row := contentRowV3{
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
	var rows []contentRowV3
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
	var row contentRowV3
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
	result := s.db.Model(&contentRowV3{}).Where("id = ? AND deleted_at IS NULL", contentID).Update("deleted_at", deletedAt.Unix())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := s.db.Model(&contentRowV3{}).Where("id = ?", contentID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
	}
	return nil
}

func contentModel(row contentRowV3) model.Content {
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
func (s *Store) SyncCommentTree(content model.Content, nodes []model.CommentNode, complete, baseline bool, batchID string, channelIDs []string) ([]model.CommentDigest, error) {
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
			isTarget := (content.Platform == model.PlatformBilibili && node.Role == model.RoleUP) || (content.Platform == model.PlatformZSXQ && node.Role == model.RoleOwner)
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
		if err := tx.Model(&contentRowV3{}).Where("id = ?", content.ID).Update("tree_incomplete", boolToInt(malformed || !complete)).Error; err != nil {
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
		payload, err := json.Marshal(digest)
		if err != nil {
			return err
		}
		key := digest.DigestKey()
		if batchID != "" {
			key += ":" + batchID
		}
		for _, channelID := range channelIDs {
			id := stableHash("comment_digest", channelID, key)
			row := outboxRow{ID: id, Kind: "comment_digest", Platform: string(content.Platform), SourceID: content.SourceID, ContentID: content.ID,
				ChannelID: channelID, IdempotencyKey: key, State: "pending", NextAt: now.Unix(), CreatedAt: now.Unix(), PayloadJSON: string(payload), ProgressJSON: "{}"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		digests = append(digests, digest)
		return nil
	})
	return digests, err
}

func (s *Store) CommentTree(contentID string) ([]model.CommentNode, bool, error) {
	var rows []commentNodeRow
	if err := s.db.Where("content_id = ?", contentID).Order("published_at, id").Find(&rows).Error; err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		var count int64
		if err := s.db.Model(&contentRowV3{}).Where("id = ?", contentID).Count(&count).Error; err != nil {
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
	var content contentRowV3
	if err := s.db.Select("tree_incomplete").Where("id = ?", contentID).Take(&content).Error; err == nil {
		incomplete = incomplete || content.TreeIncomplete != 0
	}
	return tree, incomplete, nil
}

// PromotePlatformOutbox atomically hands v3 tasks to the established channel
// dispatcher. The outbox row is removed only after the delivery row exists, so
// a crash cannot lose a task during the hand-off.
func (s *Store) PromotePlatformOutbox(now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	promoted := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var rows []outboxRow
		if err := tx.Where("state = ? AND next_at <= ?", "pending", now.Unix()).Order("next_at, id").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			delivery, err := platformDelivery(tx, row)
			if err != nil {
				return fmt.Errorf("decoding platform outbox %s: %w", row.ID, err)
			}
			if err := putDeliveryTx(tx, delivery); err != nil {
				return err
			}
			if err := tx.Where("id = ?", row.ID).Delete(&outboxRow{}).Error; err != nil {
				return err
			}
			promoted++
		}
		return nil
	})
	return promoted, err
}

func platformDelivery(tx *gorm.DB, row outboxRow) (model.Delivery, error) {
	delivery := model.Delivery{ID: row.ID, ChannelID: row.ChannelID, State: model.DeliveryState(row.State), Attempts: row.Attempts,
		NextAt: time.Unix(row.NextAt, 0), LastError: row.LastError, CreatedAt: time.Unix(row.CreatedAt, 0), OriginTraceparent: row.OriginTraceparent}
	if delivery.State == "" {
		delivery.State = model.DeliveryPending
	}
	if row.ProgressJSON != "" {
		var progress model.DeliveryProgress
		if err := json.Unmarshal([]byte(row.ProgressJSON), &progress); err == nil {
			delivery.Progress = &progress
		}
	}
	switch row.Kind {
	case "content":
		var content model.Content
		if err := json.Unmarshal([]byte(row.PayloadJSON), &content); err != nil {
			return model.Delivery{}, err
		}
		var source sourceRow
		if err := tx.Select("name").Where("id = ?", row.SourceID).Take(&source).Error; err != nil {
			return model.Delivery{}, err
		}
		delivery.Kind = model.DeliveryKindDynamic
		delivery.Dynamic = model.Dynamic{ID: content.ID, Platform: content.Platform, SourceName: source.Name,
			UID: content.SourceID, UPName: content.AuthorName,
			Type: string(content.Type), PublishedAt: content.PublishedAt, Summary: content.Text, Description: content.Text,
			URL: content.URL, Title: content.Title}
		var attachments []attachmentRow
		if err := tx.Where("content_id = ?", content.ID).Order("id").Find(&attachments).Error; err != nil {
			return model.Delivery{}, err
		}
		for _, attachment := range attachments {
			if attachment.Type == string(model.AttachmentImage) && attachment.LocalPath != "" {
				delivery.Dynamic.Media = append(delivery.Dynamic.Media, model.DynamicMedia{Kind: model.DynamicMediaImage,
					Width: attachment.Width, Height: attachment.Height, LocalPath: attachment.LocalPath, ContentType: attachment.MIME, Size: attachment.Size})
				continue
			}
			label := attachment.FileName
			if label == "" {
				label = string(model.AttachmentType(attachment.Type))
			}
			if attachment.Size > 0 {
				label += fmt.Sprintf(" (%s)", humanBytes(attachment.Size))
			}
			delivery.Dynamic.Links = append(delivery.Dynamic.Links, model.DynamicLink{Text: label, URL: content.URL})
		}
	case "comment_digest":
		var digest model.CommentDigest
		if err := json.Unmarshal([]byte(row.PayloadJSON), &digest); err != nil {
			return model.Delivery{}, err
		}
		thread := make([]model.CommentNode, 0)
		seen := make(map[string]bool)
		published := time.Time{}
		for _, path := range digest.Paths {
			for _, node := range path.Nodes {
				if !seen[node.ID] {
					node.IsTrigger = node.ID == path.TriggerID
					thread = append(thread, node)
					seen[node.ID] = true
				}
				if node.Time.After(published) {
					published = node.Time
				}
			}
		}
		var source sourceRow
		if err := tx.Select("name").Where("id = ?", row.SourceID).Take(&source).Error; err != nil {
			return model.Delivery{}, err
		}
		label, contentType := "UP主", "bilibili"
		if digest.Platform == model.PlatformZSXQ {
			label, contentType = "星球主", "zsxq"
		}
		note := model.CommentNotification{RPID: row.IdempotencyKey, Platform: digest.Platform, SourceName: source.Name,
			UPUID: digest.SourceID, UPName: label,
			ContentType: contentType, ContentID: digest.ContentID, ContentTitle: digest.Title, ContentURL: digest.ContentURL,
			PublishedAt: published, Incomplete: digest.Incomplete, Thread: thread}
		if len(digest.Triggers) == 1 {
			note.RPID = digest.Triggers[0].RPID
		}
		delivery.Kind, delivery.Comment = model.DeliveryKindComment, &note
	default:
		return model.Delivery{}, fmt.Errorf("unsupported outbox kind %q", row.Kind)
	}
	return delivery, nil
}

func humanBytes(size int64) string {
	const unit = int64(1024)
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value, suffix := float64(size), "KiB"
	for _, next := range []string{"MiB", "GiB", "TiB"} {
		value /= float64(unit)
		if value < float64(unit) {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
		suffix = next
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
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
