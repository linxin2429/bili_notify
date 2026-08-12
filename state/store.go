package state

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite" // pure-Go SQLite driver name "sqlite"
	"github.com/glebarez/sqlite"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/vault"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"
)

var (
	ErrNotFound                       = errors.New("record not found")
	ErrSourceExists                   = errors.New("source already exists")
	ErrInitialized                    = errors.New("administrator is already initialized")
	ErrRuntimeSettingsVersionMismatch = errors.New("runtime settings version mismatch")
	// ErrDeliveryNotBlocked reports that a delivery cannot be manually retried.
	ErrDeliveryNotBlocked = errors.New("delivery is not blocked")
)

const runtimeSettingsVersion = 3

type runtimeSettingsRecord struct {
	Version int `json:"_version"`
	model.RuntimeSettings
}

// Store is the single SQLite persistence layer for config, secrets, outbox, and content archive.
type Store struct {
	db    *gorm.DB
	sql   *sql.DB
	vault *vault.Vault
	path  string
}

// Open opens (or creates) the SQLite database at path, runs migrations, and returns a Store.
func Open(ctx context.Context, path string, v *vault.Vault, providers ...oteltrace.TracerProvider) (*Store, error) {
	if v == nil {
		return nil, errors.New("vault is required")
	}
	if len(providers) > 1 {
		return nil, errors.New("at most one tracer provider is allowed")
	}
	provider := oteltrace.TracerProvider(tracenoop.NewTracerProvider())
	if len(providers) == 1 && providers[0] != nil {
		// Only emit SQLite spans under an active app span. Background polls would
		// otherwise flood Tempo with root traces like "select deliveries".
		provider = parentRequiredTracerProvider{providers[0]}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("configuring database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("securing database file: %w", err)
	}
	if err := runMigrations(ctx, sqlDB, v); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	gdb, err := gorm.Open(sqlite.Dialector{
		DriverName: "sqlite",
		DSN:        path,
		Conn:       sqlDB,
	}, &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("opening gorm: %w", err)
	}
	if err := gdb.Use(tracing.NewPlugin(
		tracing.WithTracerProvider(provider),
		tracing.WithDBSystem("sqlite"),
		tracing.WithoutQueryVariables(),
		tracing.WithoutMetrics(),
	)); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("installing gorm telemetry: %w", err)
	}
	return &Store{db: gdb, sql: sqlDB, vault: v, path: path}, nil
}

// parentRequiredTracerProvider suppresses root spans so GORM/SQLite instrumentation
// only appears as children of application workflows (collection, delivery, admin, …).
type parentRequiredTracerProvider struct {
	oteltrace.TracerProvider
}

func (p parentRequiredTracerProvider) Tracer(name string, options ...oteltrace.TracerOption) oteltrace.Tracer {
	return parentRequiredTracer{Tracer: p.TracerProvider.Tracer(name, options...)}
}

type parentRequiredTracer struct {
	oteltrace.Tracer
}

func (t parentRequiredTracer) Start(ctx context.Context, spanName string, opts ...oteltrace.SpanStartOption) (context.Context, oteltrace.Span) {
	if !oteltrace.SpanContextFromContext(ctx).IsValid() {
		return ctx, oteltrace.SpanFromContext(ctx)
	}
	return t.Tracer.Start(ctx, spanName, opts...)
}

// originTraceparent returns a W3C traceparent for the active span, or "" if none.
// Stored on outbox rows so delivery can continue collection/comment traces.
func originTraceparent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

func originTracestate(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("tracestate")
}

// WithContext returns a lightweight view whose database spans inherit ctx.
func (s *Store) WithContext(ctx context.Context) *Store {
	copy := *s
	copy.db = s.db.WithContext(ctx)
	return &copy
}

func (s *Store) Close() error {
	if s == nil || s.sql == nil {
		return nil
	}
	return s.sql.Close()
}

func (s *Store) AdminPasswordHash() (string, error) {
	var row metaRow
	err := s.db.Where("key = ?", metaKeyAdminHash).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return row.Value, nil
}

func (s *Store) InitializeAdminPasswordHash(hash string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&metaRow{}).Where("key = ?", metaKeyAdminHash).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrInitialized
		}
		return tx.Create(&metaRow{Key: metaKeyAdminHash, Value: hash}).Error
	})
}

func (s *Store) SetAdminPasswordHash(hash string) error {
	res := s.db.Model(&metaRow{}).Where("key = ?", metaKeyAdminHash).Update("value", hash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RuntimeSettings() (model.RuntimeSettings, error) {
	record, err := s.runtimeSettingsRecord()
	if err != nil {
		return model.RuntimeSettings{}, err
	}
	if record.Version != runtimeSettingsVersion && record.Version != 2 {
		return model.RuntimeSettings{}, fmt.Errorf("%w: found version %d, want %d; start with a fresh data volume", ErrRuntimeSettingsVersionMismatch, record.Version, runtimeSettingsVersion)
	}
	if err := record.RuntimeSettings.Validate(); err != nil {
		return model.RuntimeSettings{}, fmt.Errorf("validating stored runtime settings: %w", err)
	}
	return record.RuntimeSettings, nil
}

func (s *Store) runtimeSettingsRecord() (runtimeSettingsRecord, error) {
	var row metaRow
	err := s.db.Where("key = ?", metaKeyRuntimeSettings).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return runtimeSettingsRecord{}, ErrNotFound
	}
	if err != nil {
		return runtimeSettingsRecord{}, err
	}
	var record runtimeSettingsRecord
	if err := json.Unmarshal([]byte(row.Value), &record); err != nil {
		return runtimeSettingsRecord{}, fmt.Errorf("decoding runtime settings: %w", err)
	}
	return record, nil
}

func (s *Store) PutRuntimeSettings(settings model.RuntimeSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(runtimeSettingsRecord{Version: runtimeSettingsVersion, RuntimeSettings: settings})
	if err != nil {
		return fmt.Errorf("encoding runtime settings: %w", err)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if settings.AIAutoProcessingEnabled {
			if _, _, _, err := s.defaultAIConfigTx(tx); err != nil {
				return fmt.Errorf("enabling automatic AI processing: %w", err)
			}
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).Create(&metaRow{Key: metaKeyRuntimeSettings, Value: string(raw)}).Error
	})
}

func (s *Store) ListUPs() ([]model.UP, error) {
	var rows []sourceRow
	if err := s.db.Where("platform = ?", model.PlatformBilibili).Find(&rows).Error; err != nil {
		return nil, err
	}
	ups := make([]model.UP, 0, len(rows))
	for _, row := range rows {
		ups = append(ups, upFromSourceRow(row))
	}
	if err := s.enrichUPRouting(ups); err != nil {
		return nil, err
	}
	slices.SortFunc(ups, func(a, b model.UP) int { return a.UIDCompare(b) })
	return ups, nil
}

func (s *Store) UP(uid string) (model.UP, error) {
	var row sourceRow
	err := s.db.Where("id = ?", model.SourceID(model.PlatformBilibili, uid)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.UP{}, ErrNotFound
	}
	if err != nil {
		return model.UP{}, err
	}
	ups := []model.UP{upFromSourceRow(row)}
	if err := s.enrichUPRouting(ups); err != nil {
		return model.UP{}, err
	}
	return ups[0], nil
}

func (s *Store) PutUP(up model.UP) error {
	if err := up.Validate(); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		sourceID := model.SourceID(model.PlatformBilibili, up.UID)
		if err := tx.Model(&sourceRow{}).Where("id = ?", sourceID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			var total int64
			if err := tx.Model(&sourceRow{}).Where("platform = ?", model.PlatformBilibili).Count(&total).Error; err != nil {
				return err
			}
			if total >= 100 {
				return errors.New("at most 100 UPs can be configured")
			}
		}
		wasEnabled := false
		if count > 0 {
			var existing sourceRow
			if err := tx.Where("id = ?", sourceID).Take(&existing).Error; err != nil {
				return err
			}
			wasEnabled = existing.Enabled != 0
		}
		source := model.Source{
			ID: sourceID, Platform: model.PlatformBilibili,
			Type: model.SourceBilibiliUP, ExternalID: up.UID, Name: up.Name, Enabled: up.Enabled,
			BaselineState: model.BaselinePending,
		}
		if up.BaselineReady {
			source.BaselineState = model.BaselineComplete
		}
		sourceRow := sourceFromModel(source)
		sourceRow.ExclusiveBaselineReady = boolToInt(up.ExclusiveBaselineReady)
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "enabled", "baseline_state", "exclusive_baseline_ready"}),
		}).Create(&sourceRow).Error; err != nil {
			return err
		}
		if !wasEnabled && up.Enabled {
			return tx.Model(&upFollowRelationRow{}).Where("up_uid = ?", up.UID).Update("space_synced", 0).Error
		}
		return nil
	})
}

func (s *Store) DeleteUP(uid string) error {
	return s.deleteSource(model.SourceID(model.PlatformBilibili, uid), uid)
}

func (s *Store) SetUPResult(uid, name string, at time.Time, pollErr error) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row sourceRow
		err := tx.Where("id = ?", model.SourceID(model.PlatformBilibili, uid)).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		up := upFromSourceRow(row)
		up.LastPollAt = at
		if name != "" {
			up.Name = name
		}
		if pollErr == nil {
			up.LastSuccessAt = at
			up.LastError = ""
			up.ConsecutiveFail = 0
		} else {
			up.LastError = pollErr.Error()
			up.ConsecutiveFail++
		}
		updates := map[string]any{"name": up.Name, "last_poll_at": at.Unix(), "last_error": up.LastError, "consecutive_fails": up.ConsecutiveFail}
		if pollErr == nil {
			updates["last_success_at"] = at.Unix()
		}
		return tx.Model(&sourceRow{}).Where("id = ?", model.SourceID(model.PlatformBilibili, uid)).Updates(updates).Error
	})
}

func upFromSourceRow(row sourceRow) model.UP {
	return model.UP{
		UID: row.ExternalID, Name: row.Name, Enabled: row.Enabled != 0,
		BaselineReady:          row.BaselineState == string(model.BaselineComplete),
		ExclusiveBaselineReady: row.ExclusiveBaselineReady != 0,
		LastPollAt:             pointerTime(row.LastPollAt), LastSuccessAt: pointerTime(row.LastSuccessAt),
		LastError: row.LastError, ConsecutiveFail: row.ConsecutiveFails,
		FollowState: model.FollowUnknown, CollectionRoute: model.CollectionRouteSpace,
	}
}

func (s *Store) ListChannels() ([]model.Channel, error) {
	var rows []channelRow
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	channels := make([]model.Channel, 0, len(rows))
	for _, row := range rows {
		ch, err := s.channelFromRow(row)
		if err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	slices.SortFunc(channels, func(a, b model.Channel) int { return a.NameCompare(b) })
	return channels, nil
}

func (s *Store) Channel(id string) (model.Channel, error) {
	var row channelRow
	err := s.db.Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Channel{}, ErrNotFound
	}
	if err != nil {
		return model.Channel{}, err
	}
	return s.channelFromRow(row)
}

func (s *Store) PutChannel(ch model.Channel) (model.Channel, error) {
	if err := ch.Validate(); err != nil {
		return model.Channel{}, err
	}
	now := time.Now()
	if ch.ID == "" {
		id, err := randomID()
		if err != nil {
			return model.Channel{}, err
		}
		ch.ID = id
		ch.CreatedAt = now
	}
	ch.UpdatedAt = now
	public, secret := splitChannelSettings(ch.Settings)
	publicJSON, err := json.Marshal(public)
	if err != nil {
		return model.Channel{}, err
	}
	sealed, err := sealJSON(s.vault, tableChannelSecrets, ch.ID, secret)
	if err != nil {
		return model.Channel{}, err
	}
	row := channelRow{ID: ch.ID, Name: ch.Name, Type: string(ch.Type), Enabled: boolToInt(ch.Enabled),
		PublicSettingsJSON: string(publicJSON), SecretSealed: sealed, CreatedAt: ch.CreatedAt.Unix(), UpdatedAt: ch.UpdatedAt.Unix()}
	if err := s.db.Save(&row).Error; err != nil {
		return model.Channel{}, err
	}
	return ch, nil
}

func (s *Store) UpdateChannelSettings(id string, settings map[string]string) (model.Channel, error) {
	var channel model.Channel
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row channelRow
		if err := tx.Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var err error
		channel, err = s.channelFromRow(row)
		if err != nil {
			return err
		}
		if channel.Settings == nil {
			channel.Settings = make(map[string]string)
		}
		for key, value := range settings {
			channel.Settings[key] = value
		}
		channel.UpdatedAt = time.Now()
		if err := channel.Validate(); err != nil {
			return err
		}
		public, secret := splitChannelSettings(channel.Settings)
		publicJSON, err := json.Marshal(public)
		if err != nil {
			return err
		}
		sealed, err := sealJSON(s.vault, tableChannelSecrets, channel.ID, secret)
		if err != nil {
			return err
		}
		return tx.Save(&channelRow{ID: channel.ID, Name: channel.Name, Type: string(channel.Type), Enabled: boolToInt(channel.Enabled),
			PublicSettingsJSON: string(publicJSON), SecretSealed: sealed, CreatedAt: channel.CreatedAt.Unix(), UpdatedAt: channel.UpdatedAt.Unix()}).Error
	})
	return channel, err
}

func (s *Store) channelFromRow(row channelRow) (model.Channel, error) {
	settings := make(map[string]string)
	if err := json.Unmarshal([]byte(row.PublicSettingsJSON), &settings); err != nil {
		return model.Channel{}, fmt.Errorf("decoding channel %s public settings: %w", row.ID, err)
	}
	var secret map[string]string
	if err := openJSON(s.vault, tableChannelSecrets, row.ID, row.SecretSealed, &secret); err != nil {
		return model.Channel{}, fmt.Errorf("opening channel %s secrets: %w", row.ID, err)
	}
	for key, value := range secret {
		settings[key] = value
	}
	return model.Channel{ID: row.ID, Name: row.Name, Type: model.ChannelType(row.Type), Enabled: row.Enabled != 0,
		Settings: settings, CreatedAt: time.Unix(row.CreatedAt, 0), UpdatedAt: time.Unix(row.UpdatedAt, 0)}, nil
}

func (s *Store) DeleteChannel(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&outboxRow{}).Where("channel_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("channel has pending deliveries")
		}
		res := tx.Where("id = ?", id).Delete(&channelRow{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) SaveSession(session model.BiliSession) error {
	sealedCookies, err := sealJSON(s.vault, tablePlatformAccounts, string(model.PlatformBilibili), session.Cookies)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var previous platformAccountRow
		err := tx.Where("platform = ?", model.PlatformBilibili).Take(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := session.UpdatedAt
		if now.IsZero() {
			now = time.Now()
		}
		status := model.AccountInvalid
		var verifiedAt *int64
		if session.AccountUID != "" {
			status = model.AccountConnected
			verifiedAt = unixPointer(now)
		}
		account := platformAccountRow{
			Platform:      string(model.PlatformBilibili),
			ExternalID:    session.AccountUID,
			DisplayName:   session.AccountName,
			Status:        string(status),
			SealedSession: sealedCookies,
			SealedAAD:     tablePlatformAccounts,
			VerifiedAt:    verifiedAt,
			UpdatedAt:     now.Unix(),
		}
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
		if session.AccountUID == "" || session.AccountUID == previous.ExternalID {
			return nil
		}
		if err := tx.Where("1 = 1").Delete(&upFollowRelationRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("1 = 1").Delete(&biliFeedStateRow{}).Error; err != nil {
			return err
		}
		return tx.Create(&biliFeedStateRow{AccountUID: session.AccountUID, UpdatedAt: time.Now().Unix()}).Error
	})
}

func (s *Store) Session() (model.BiliSession, error) {
	var row platformAccountRow
	err := s.db.Where("platform = ?", model.PlatformBilibili).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BiliSession{}, ErrNotFound
	}
	if err != nil {
		return model.BiliSession{}, err
	}
	var cookies map[string]string
	if err := openJSON(s.vault, tablePlatformAccounts, string(model.PlatformBilibili), row.SealedSession, &cookies); err != nil {
		return model.BiliSession{}, err
	}
	return model.BiliSession{Cookies: cookies, AccountUID: row.ExternalID, AccountName: row.DisplayName, UpdatedAt: time.Unix(row.UpdatedAt, 0)}, nil
}

func (s *Store) ClearSession() error {
	return s.db.Where("platform = ?", model.PlatformBilibili).Delete(&platformAccountRow{}).Error
}

func (s *Store) Seen(uid, dynamicID string) (bool, error) {
	var count int64
	err := s.db.Model(&seenItemRow{}).Where("platform = ? AND source_id = ? AND entity_type = ? AND entity_id = ?",
		model.PlatformBilibili, model.SourceID(model.PlatformBilibili, uid), "content", model.ContentID(model.PlatformBilibili, dynamicID)).Count(&count).Error
	return count > 0, err
}

// DynamicBaselineMode controls which newly seen dynamics are archived without delivery.
type DynamicBaselineMode uint8

const (
	DynamicBaselineNone DynamicBaselineMode = iota
	DynamicBaselineAll
	DynamicBaselineExclusive
)

func (m DynamicBaselineMode) includes(dynamic model.Dynamic) bool {
	return m == DynamicBaselineAll || (m == DynamicBaselineExclusive && dynamic.Exclusive)
}

// RecordDynamics archives full content and atomically marks unseen dynamics and creates deliveries.
func (s *Store) RecordDynamics(uid string, dynamics []model.Dynamic, _ []string, baselineMode DynamicBaselineMode) (int, error) {
	created := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		autoAI, err := automaticAIEnabledTx(tx)
		if err != nil {
			return err
		}
		existing := make(map[string]bool, len(dynamics))
		for _, dynamic := range dynamics {
			if dynamic.ID == "" {
				continue
			}
			var count int64
			contentID := model.ContentID(model.PlatformBilibili, dynamic.ID)
			if err := tx.Model(&contentRow{}).Where("id = ?", contentID).Count(&count).Error; err != nil {
				return err
			}
			existing[contentID] = count != 0
		}
		if err := archiveDynamicsTx(tx, dynamics, baselineMode); err != nil {
			return err
		}
		channelIDs, err := enabledChannelIDsTx(tx)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, dynamic := range dynamics {
			if dynamic.ID == "" {
				continue
			}
			contentID := model.ContentID(model.PlatformBilibili, dynamic.ID)
			if existing[contentID] {
				continue
			}
			if baselineMode.includes(dynamic) {
				continue
			}
			origin := originTraceparent(tx.Statement.Context)
			snapshot := dynamic
			snapshot.ID = contentID
			snapshot.Platform = model.PlatformBilibili
			snapshot.UID = model.SourceID(model.PlatformBilibili, uid)
			for _, channelID := range channelIDs {
				d := model.Delivery{
					ID:                stableHash("content", contentID, channelID),
					Kind:              model.DeliveryKindDynamic,
					Dynamic:           snapshot,
					ChannelID:         channelID,
					State:             model.DeliveryPending,
					NextAt:            now,
					CreatedAt:         now,
					OriginTraceparent: origin,
				}
				if err := putDeliveryTx(tx, d); err != nil {
					return err
				}
			}
			if autoAI {
				if _, err := s.createAutomaticAIJobsTx(tx, dynamic, model.SourceID(model.PlatformBilibili, uid), channelIDs); err != nil {
					return fmt.Errorf("creating automatic AI pipeline for dynamic %s: %w", dynamic.ID, err)
				}
			}
			created++
		}
		if baselineMode != DynamicBaselineNone {
			var row sourceRow
			if err := tx.Where("id = ?", model.SourceID(model.PlatformBilibili, uid)).Take(&row).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrNotFound
				}
				return err
			}
			up := upFromSourceRow(row)
			if baselineMode == DynamicBaselineAll {
				up.BaselineReady = true
			}
			up.ExclusiveBaselineReady = true
			updates := map[string]any{"exclusive_baseline_ready": boolToInt(up.ExclusiveBaselineReady)}
			if baselineMode == DynamicBaselineAll {
				updates["baseline_state"] = model.BaselineComplete
			}
			return tx.Model(&sourceRow{}).Where("id = ?", model.SourceID(model.PlatformBilibili, uid)).Updates(updates).Error
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

// UpsertCommentTargets merges discovered commentable contents for one UP and keeps the newest n.
func (s *Store) UpsertCommentTargets(uid string, discovered []model.CommentTarget, n int) ([]model.CommentTarget, error) {
	if n < 1 {
		return nil, fmt.Errorf("comment track n must be positive")
	}
	var kept []model.CommentTarget
	err := s.db.Transaction(func(tx *gorm.DB) error {
		sourceID := model.SourceID(model.PlatformBilibili, uid)
		var rows []syncTargetRow
		if err := tx.Where("platform = ? AND source_id = ?", model.PlatformBilibili, sourceID).Find(&rows).Error; err != nil {
			return err
		}
		byKey := make(map[string]model.CommentTarget, len(rows)+len(discovered))
		for _, row := range rows {
			t := row.toCommentTarget()
			byKey[t.Key()] = t
		}
		for _, target := range discovered {
			if target.CommentType <= 0 || target.CommentOID == "" {
				continue
			}
			target.UID = uid
			key := target.Key()
			if prev, ok := byKey[key]; ok {
				target.BaselineReady = prev.BaselineReady
				target.Closed = prev.Closed
				target.LastPollAt = prev.LastPollAt
				target.LastError = prev.LastError
			}
			byKey[key] = target
		}
		merged := make([]model.CommentTarget, 0, len(byKey))
		for _, target := range byKey {
			merged = append(merged, target)
		}
		slices.SortFunc(merged, func(a, b model.CommentTarget) int {
			if c := b.PublishedAt.Compare(a.PublishedAt); c != 0 {
				return c
			}
			return cmp.Compare(a.Key(), b.Key())
		})
		if len(merged) > n {
			merged = merged[:n]
		}
		kept = merged
		if err := tx.Where("platform = ? AND source_id = ?", model.PlatformBilibili, sourceID).Delete(&syncTargetRow{}).Error; err != nil {
			return err
		}
		for _, target := range kept {
			row := syncTargetFromModel(target)
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return kept, s.enrichCommentTargets(kept)
}

func (s *Store) ListCommentTargets(uid string) ([]model.CommentTarget, error) {
	var rows []syncTargetRow
	if err := s.db.Where("platform = ? AND source_id = ?", model.PlatformBilibili, model.SourceID(model.PlatformBilibili, uid)).Find(&rows).Error; err != nil {
		return nil, err
	}
	targets := make([]model.CommentTarget, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, row.toCommentTarget())
	}
	return targets, s.enrichCommentTargets(targets)
}

func (s *Store) ListAllCommentTargets() ([]model.CommentTarget, error) {
	var rows []syncTargetRow
	if err := s.db.Where("platform = ?", model.PlatformBilibili).Find(&rows).Error; err != nil {
		return nil, err
	}
	targets := make([]model.CommentTarget, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, row.toCommentTarget())
	}
	return targets, s.enrichCommentTargets(targets)
}

func (s *Store) enrichCommentTargets(targets []model.CommentTarget) error {
	for index := range targets {
		var source sourceRow
		if err := s.db.Select("name").Where("id = ?", model.SourceID(model.PlatformBilibili, targets[index].UID)).Take(&source).Error; err != nil {
			return err
		}
		var content contentRow
		if err := s.db.Select("upstream_type").Where("id = ?", model.ContentID(model.PlatformBilibili, targets[index].DynamicID)).Take(&content).Error; err != nil {
			return err
		}
		targets[index].UPName = source.Name
		targets[index].ContentType = content.UpstreamType
	}
	return nil
}

func (s *Store) PutCommentTargets(uid string, targets []model.CommentTarget) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("platform = ? AND source_id = ?", model.PlatformBilibili, model.SourceID(model.PlatformBilibili, uid)).Delete(&syncTargetRow{}).Error; err != nil {
			return err
		}
		for _, target := range targets {
			target.UID = uid
			row := syncTargetFromModel(target)
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) UpdateCommentTarget(target model.CommentTarget) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row syncTargetRow
		err := tx.Where("platform = ? AND source_id = ? AND comment_type = ? AND comment_oid = ?", model.PlatformBilibili,
			model.SourceID(model.PlatformBilibili, target.UID), target.CommentType, target.CommentOID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		updated := syncTargetFromModel(target)
		return tx.Save(&updated).Error
	})
}

func (s *Store) CommentSeen(uid, rpid string) (bool, error) {
	var count int64
	err := s.db.Model(&seenItemRow{}).Where("platform = ? AND source_id = ? AND entity_type = ? AND entity_id = ?", model.PlatformBilibili,
		model.SourceID(model.PlatformBilibili, uid), "comment", model.CommentID(model.PlatformBilibili, rpid)).Count(&count).Error
	return count > 0, err
}

func (s *Store) ListDeliveries(limit int) ([]model.Delivery, error) {
	var rows []outboxRow
	q := s.db.Order("next_at ASC, id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeDeliveries(rows)
}

// DeliveryQuery selects one stable immutable created-at/id ordered page.
type DeliveryQuery struct {
	Limit          int
	AfterCreatedAt time.Time
	AfterID        string
}

type DeliverySummary struct {
	ID        string              `json:"id"`
	Kind      string              `json:"kind"`
	Platform  model.Platform      `json:"platform,omitempty"`
	SourceID  string              `json:"source_id,omitempty"`
	ContentID string              `json:"content_id,omitempty"`
	ChannelID string              `json:"channel_id"`
	State     model.DeliveryState `json:"state"`
	Attempts  int                 `json:"attempts"`
	NextAt    time.Time           `json:"next_at"`
	LastError string              `json:"last_error,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
	Title     string              `json:"title,omitempty"`
	Summary   string              `json:"summary,omitempty"`
}

func (s *Store) QueryDeliverySummaries(query DeliveryQuery) ([]DeliverySummary, error) {
	var rows []outboxRow
	db := s.db.Select("id", "kind", "platform", "source_id", "content_id", "channel_id", "state", "attempts", "next_at", "last_error", "created_at", "title", "summary").Order("created_at DESC, id DESC")
	if !query.AfterCreatedAt.IsZero() && strings.TrimSpace(query.AfterID) != "" {
		db = db.Where("created_at < ? OR (created_at = ? AND id < ?)", query.AfterCreatedAt.Unix(), query.AfterCreatedAt.Unix(), query.AfterID)
	}
	if query.Limit > 0 {
		db = db.Limit(query.Limit)
	}
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]DeliverySummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, DeliverySummary{ID: row.ID, Kind: row.Kind, Platform: model.Platform(row.Platform), SourceID: row.SourceID,
			ContentID: row.ContentID, ChannelID: row.ChannelID, State: model.DeliveryState(row.State), Attempts: row.Attempts,
			NextAt: time.Unix(row.NextAt, 0), LastError: row.LastError, CreatedAt: time.Unix(row.CreatedAt, 0), Title: row.Title, Summary: row.Summary})
	}
	return items, nil
}

// QueryDeliveries returns deliveries ordered by immutable creation time and id
// so scheduling updates cannot move an item across cursor boundaries.
func (s *Store) QueryDeliveries(query DeliveryQuery) ([]model.Delivery, error) {
	var rows []outboxRow
	q := s.db.Order("created_at DESC, id DESC")
	if !query.AfterCreatedAt.IsZero() && strings.TrimSpace(query.AfterID) != "" {
		q = q.Where("created_at < ? OR (created_at = ? AND id < ?)", query.AfterCreatedAt.Unix(), query.AfterCreatedAt.Unix(), query.AfterID)
	}
	if query.Limit > 0 {
		q = q.Limit(query.Limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeDeliveries(rows)
}

func (s *Store) DueDeliveries(now time.Time, limit int) ([]model.Delivery, error) {
	var rows []outboxRow
	q := s.db.Where("state = ? AND next_at <= ?", string(model.DeliveryPending), now.Unix()).Order("next_at ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeDeliveries(rows)
}

type OutboxStats struct {
	Pending int64
	Blocked int64
	Oldest  time.Time
}

func (s *Store) DeliveryStats() (OutboxStats, error) {
	var row struct {
		Pending       int64  `gorm:"column:pending"`
		Blocked       int64  `gorm:"column:blocked"`
		OldestCreated *int64 `gorm:"column:oldest_created"`
	}
	err := s.db.Model(&outboxRow{}).Select(
		"COALESCE(SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), 0) AS pending, " +
			"COALESCE(SUM(CASE WHEN state = 'blocked' THEN 1 ELSE 0 END), 0) AS blocked, MIN(created_at) AS oldest_created").Scan(&row).Error
	if err != nil {
		return OutboxStats{}, err
	}
	stats := OutboxStats{Pending: row.Pending, Blocked: row.Blocked}
	if row.OldestCreated != nil {
		stats.Oldest = time.Unix(*row.OldestCreated, 0)
	}
	return stats, nil
}

func (s *Store) CompleteDelivery(id string) error {
	return s.db.Where("id = ?", id).Delete(&outboxRow{}).Error
}

// RetryDelivery makes one blocked delivery immediately eligible for the
// background dispatcher without discarding its attempt history or progress.
func (s *Store) RetryDelivery(id string, now time.Time) error {
	result := s.db.Model(&outboxRow{}).Where("id = ? AND state = ?", id, string(model.DeliveryBlocked)).Updates(map[string]any{
		"state":   string(model.DeliveryPending),
		"next_at": now.Unix(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	var count int64
	if err := s.db.Model(&outboxRow{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return ErrDeliveryNotBlocked
}

func (s *Store) FailDelivery(id string, blocked bool, next time.Time, deliveryErr error, progress *model.DeliveryProgress) error {
	progressJSON, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	updates := map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_at": next.Unix(), "last_error": deliveryErr.Error(), "progress_json": string(progressJSON)}
	if blocked {
		updates["state"] = model.DeliveryBlocked
	}
	result := s.db.Model(&outboxRow{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UnblockChannel(channelID string) error {
	return s.db.Model(&outboxRow{}).Where("channel_id = ? AND state = ?", channelID, model.DeliveryBlocked).
		Updates(map[string]any{"state": model.DeliveryPending, "next_at": time.Now().Unix()}).Error
}

func putDeliveryTx(tx *gorm.DB, d model.Delivery) error {
	platform, sourceID, contentID, err := validateDeliverySnapshot(d)
	if err != nil {
		return fmt.Errorf("validating delivery: %w", err)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encoding delivery: %w", err)
	}
	kind, title, summary := "content", d.Dynamic.Title, d.Dynamic.Summary
	switch d.Kind {
	case model.DeliveryKindComment:
		kind, title = "comment", d.Comment.ContentTitle
		if len(d.Comment.Thread) != 0 {
			summary = d.Comment.Thread[len(d.Comment.Thread)-1].Message
		}
	case model.DeliveryKindAI:
		kind, title, summary = "ai", d.AI.Title, d.AI.Body
	default:
		if d.Dynamic.UID == "system" {
			kind = "system"
		}
	}
	progress, err := json.Marshal(d.Progress)
	if err != nil {
		return fmt.Errorf("encoding delivery progress: %w", err)
	}
	row := outboxRow{
		ID: d.ID, Kind: kind, Platform: platform, SourceID: sourceID, ContentID: contentID,
		ChannelID: d.ChannelID, IdempotencyKey: d.ID, State: string(d.State), Attempts: d.Attempts,
		NextAt: d.NextAt.Unix(), LastError: d.LastError, CreatedAt: d.CreatedAt.Unix(), Title: title,
		Summary: summary, PayloadJSON: string(raw), ProgressJSON: string(progress), OriginTraceparent: d.OriginTraceparent,
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"state", "attempts", "next_at", "last_error", "progress_json"}),
	}).Create(&row).Error
}

func decodeDeliveries(rows []outboxRow) ([]model.Delivery, error) {
	out := make([]model.Delivery, 0, len(rows))
	for _, row := range rows {
		d, err := decodeDelivery(row)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func decodeDelivery(row outboxRow) (model.Delivery, error) {
	var d model.Delivery
	if err := json.Unmarshal([]byte(row.PayloadJSON), &d); err != nil {
		return model.Delivery{}, fmt.Errorf("decoding delivery %s: %w", row.ID, err)
	}
	// Prefer column values for scheduling fields in case payload is stale.
	d.ID = row.ID
	d.ChannelID = row.ChannelID
	d.State = model.DeliveryState(row.State)
	d.Attempts = row.Attempts
	d.NextAt = time.Unix(row.NextAt, 0)
	d.LastError = row.LastError
	d.CreatedAt = time.Unix(row.CreatedAt, 0)
	switch row.Kind {
	case "content", "system":
		d.Kind = model.DeliveryKindDynamic
	case "comment":
		d.Kind = model.DeliveryKindComment
	case "ai":
		d.Kind = model.DeliveryKindAI
	default:
		return model.Delivery{}, fmt.Errorf("decoding delivery %s: unsupported kind %q", row.ID, row.Kind)
	}
	if row.ProgressJSON != "" {
		if err := json.Unmarshal([]byte(row.ProgressJSON), &d.Progress); err != nil {
			return model.Delivery{}, fmt.Errorf("decoding delivery %s progress: %w", row.ID, err)
		}
	}
	d.OriginTraceparent = row.OriginTraceparent
	return d, nil
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating channel ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
