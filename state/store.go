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
	ErrInitialized                    = errors.New("administrator is already initialized")
	ErrRuntimeSettingsVersionMismatch = errors.New("runtime settings version mismatch")
	// ErrDeliveryNotBlocked reports that a delivery cannot be manually retried.
	ErrDeliveryNotBlocked = errors.New("delivery is not blocked")
)

const runtimeSettingsVersion = 2

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
	if err := runMigrations(ctx, sqlDB); err != nil {
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
	if record.Version != runtimeSettingsVersion {
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
	var rows []upRow
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	ups := make([]model.UP, 0, len(rows))
	for _, row := range rows {
		ups = append(ups, row.toModel())
	}
	if err := s.enrichUPRouting(ups); err != nil {
		return nil, err
	}
	slices.SortFunc(ups, func(a, b model.UP) int { return a.UIDCompare(b) })
	return ups, nil
}

func (s *Store) UP(uid string) (model.UP, error) {
	var row upRow
	err := s.db.Where("uid = ?", uid).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.UP{}, ErrNotFound
	}
	if err != nil {
		return model.UP{}, err
	}
	ups := []model.UP{row.toModel()}
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
		if err := tx.Model(&upRow{}).Where("uid = ?", up.UID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			var total int64
			if err := tx.Model(&upRow{}).Count(&total).Error; err != nil {
				return err
			}
			if total >= 100 {
				return errors.New("at most 100 UPs can be configured")
			}
		}
		wasEnabled := false
		if count > 0 {
			var existing upRow
			if err := tx.Where("uid = ?", up.UID).Take(&existing).Error; err != nil {
				return err
			}
			wasEnabled = existing.Enabled != 0
		}
		row := upFromModel(up)
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if !wasEnabled && up.Enabled {
			return tx.Model(&upFollowRelationRow{}).Where("up_uid = ?", up.UID).Update("space_synced", 0).Error
		}
		return nil
	})
}

func (s *Store) DeleteUP(uid string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var dynamicIDs []string
		if err := tx.Model(&dynamicRow{}).Where("uid = ?", uid).Pluck("id", &dynamicIDs).Error; err != nil {
			return err
		}
		dynamicSet := make(map[string]struct{}, len(dynamicIDs))
		for _, id := range dynamicIDs {
			dynamicSet[id] = struct{}{}
		}
		var deliveryRows []deliveryRow
		if err := tx.Find(&deliveryRows).Error; err != nil {
			return err
		}
		for _, row := range deliveryRows {
			delivery, err := decodeDelivery(row)
			if err != nil {
				return err
			}
			belongsToUP := delivery.EffectiveKind() == model.DeliveryKindDynamic && delivery.Dynamic.UID == uid
			if delivery.EffectiveKind() == model.DeliveryKindComment && delivery.Comment != nil {
				belongsToUP = delivery.Comment.UPUID == uid
			}
			if delivery.EffectiveKind() == model.DeliveryKindAI && delivery.AI != nil {
				_, belongsToUP = dynamicSet[delivery.AI.DynamicID]
			}
			if belongsToUP {
				if err := tx.Where("id = ?", row.ID).Delete(&deliveryRow{}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Where("uid = ?", uid).Delete(&upRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("uid = ?", uid).Delete(&seenDynamicRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("uid = ?", uid).Delete(&seenCommentRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("uid = ?", uid).Delete(&commentTargetRow{}).Error; err != nil {
			return err
		}
		if len(dynamicIDs) > 0 {
			if err := tx.Where("source_dynamic_id IN ?", dynamicIDs).Delete(&aiJobRow{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("uid = ?", uid).Delete(&dynamicRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("up_uid = ?", uid).Delete(&commentRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("up_uid = ?", uid).Delete(&upFollowRelationRow{}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) SetUPResult(uid, name string, at time.Time, pollErr error) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row upRow
		err := tx.Where("uid = ?", uid).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		up := row.toModel()
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
		row = upFromModel(up)
		return tx.Save(&row).Error
	})
}

func (s *Store) ListChannels() ([]model.Channel, error) {
	var rows []channelRow
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	channels := make([]model.Channel, 0, len(rows))
	for _, row := range rows {
		var ch model.Channel
		if err := openJSON(s.vault, tableChannels, row.ID, row.Sealed, &ch); err != nil {
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
	var ch model.Channel
	if err := openJSON(s.vault, tableChannels, row.ID, row.Sealed, &ch); err != nil {
		return model.Channel{}, err
	}
	return ch, nil
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
	sealed, err := sealJSON(s.vault, tableChannels, ch.ID, ch)
	if err != nil {
		return model.Channel{}, err
	}
	if err := s.db.Save(&channelRow{ID: ch.ID, Sealed: sealed}).Error; err != nil {
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
		if err := openJSON(s.vault, tableChannels, row.ID, row.Sealed, &channel); err != nil {
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
		sealed, err := sealJSON(s.vault, tableChannels, channel.ID, channel)
		if err != nil {
			return err
		}
		return tx.Save(&channelRow{ID: channel.ID, Sealed: sealed}).Error
	})
	return channel, err
}

func (s *Store) DeleteChannel(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&deliveryRow{}).Where("channel_id = ?", id).Count(&count).Error; err != nil {
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
	sealed, err := sealJSON(s.vault, tableAuthSession, authSessionID, session)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var previous model.BiliSession
		var row authSessionRow
		err := tx.Where("id = ?", authSessionID).Take(&row).Error
		if err == nil {
			if err := openJSON(s.vault, tableAuthSession, authSessionID, row.Sealed, &previous); err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Save(&authSessionRow{ID: authSessionID, Sealed: sealed}).Error; err != nil {
			return err
		}
		if session.AccountUID == "" || session.AccountUID == previous.AccountUID {
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
	var row authSessionRow
	err := s.db.Where("id = ?", authSessionID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BiliSession{}, ErrNotFound
	}
	if err != nil {
		return model.BiliSession{}, err
	}
	var session model.BiliSession
	if err := openJSON(s.vault, tableAuthSession, authSessionID, row.Sealed, &session); err != nil {
		return model.BiliSession{}, err
	}
	return session, nil
}

func (s *Store) ClearSession() error {
	return s.db.Where("id = ?", authSessionID).Delete(&authSessionRow{}).Error
}

func (s *Store) Seen(uid, dynamicID string) (bool, error) {
	var count int64
	err := s.db.Model(&seenDynamicRow{}).Where("uid = ? AND dynamic_id = ?", uid, dynamicID).Count(&count).Error
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
func (s *Store) RecordDynamics(uid string, dynamics []model.Dynamic, channelIDs []string, baselineMode DynamicBaselineMode) (int, error) {
	created := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		autoAI, err := automaticAIEnabledTx(tx)
		if err != nil {
			return err
		}
		if err := archiveDynamicsTx(tx, dynamics, baselineMode); err != nil {
			return err
		}
		now := time.Now()
		for _, dynamic := range dynamics {
			if dynamic.ID == "" {
				continue
			}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seenDynamicRow{
				UID:       uid,
				DynamicID: dynamic.ID,
				SeenAt:    dynamic.PublishedAt.Unix(),
			})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			if baselineMode.includes(dynamic) {
				continue
			}
			origin := originTraceparent(tx.Statement.Context)
			for _, channelID := range channelIDs {
				d := model.Delivery{
					ID:                dynamic.ID + ":" + channelID,
					Kind:              model.DeliveryKindDynamic,
					Dynamic:           dynamic,
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
				if _, err := s.createAutomaticAIJobsTx(tx, dynamic, channelIDs); err != nil {
					return fmt.Errorf("creating automatic AI pipeline for dynamic %s: %w", dynamic.ID, err)
				}
			}
			created++
		}
		if baselineMode != DynamicBaselineNone {
			var row upRow
			if err := tx.Where("uid = ?", uid).Take(&row).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrNotFound
				}
				return err
			}
			up := row.toModel()
			if baselineMode == DynamicBaselineAll {
				up.BaselineReady = true
			}
			up.ExclusiveBaselineReady = true
			updated := upFromModel(up)
			return tx.Save(&updated).Error
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
		var rows []commentTargetRow
		if err := tx.Where("uid = ?", uid).Find(&rows).Error; err != nil {
			return err
		}
		byKey := make(map[string]model.CommentTarget, len(rows)+len(discovered))
		for _, row := range rows {
			t := row.toModel()
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
		if err := tx.Where("uid = ?", uid).Delete(&commentTargetRow{}).Error; err != nil {
			return err
		}
		for _, target := range kept {
			row := commentTargetFromModel(target)
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return kept, err
}

func (s *Store) ListCommentTargets(uid string) ([]model.CommentTarget, error) {
	var rows []commentTargetRow
	if err := s.db.Where("uid = ?", uid).Find(&rows).Error; err != nil {
		return nil, err
	}
	targets := make([]model.CommentTarget, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, row.toModel())
	}
	return targets, nil
}

func (s *Store) ListAllCommentTargets() ([]model.CommentTarget, error) {
	var rows []commentTargetRow
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	targets := make([]model.CommentTarget, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, row.toModel())
	}
	return targets, nil
}

func (s *Store) PutCommentTargets(uid string, targets []model.CommentTarget) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("uid = ?", uid).Delete(&commentTargetRow{}).Error; err != nil {
			return err
		}
		for _, target := range targets {
			target.UID = uid
			row := commentTargetFromModel(target)
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) UpdateCommentTarget(target model.CommentTarget) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row commentTargetRow
		err := tx.Where("uid = ? AND comment_type = ? AND comment_oid = ?", target.UID, target.CommentType, target.CommentOID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		updated := commentTargetFromModel(target)
		return tx.Save(&updated).Error
	})
}

func (s *Store) CommentSeen(uid, rpid string) (bool, error) {
	var count int64
	err := s.db.Model(&seenCommentRow{}).Where("uid = ? AND rpid = ?", uid, rpid).Count(&count).Error
	return count > 0, err
}

// RecordCommentNotifications archives full threads, marks UP reply rpids seen, and enqueues deliveries.
func (s *Store) RecordCommentNotifications(target model.CommentTarget, notes []model.CommentNotification, channelIDs []string, baseline bool) (int, error) {
	created := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := archiveCommentsTx(tx, notes, baseline); err != nil {
			return err
		}
		now := time.Now()
		for _, note := range notes {
			if note.RPID == "" {
				continue
			}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seenCommentRow{
				UID:    target.UID,
				RPID:   note.RPID,
				SeenAt: note.PublishedAt.Unix(),
			})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			if baseline {
				continue
			}
			origin := originTraceparent(tx.Statement.Context)
			for _, channelID := range channelIDs {
				n := note
				d := model.Delivery{
					ID:                "comment:" + note.RPID + ":" + channelID,
					Kind:              model.DeliveryKindComment,
					Comment:           &n,
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
			created++
		}
		var row commentTargetRow
		err := tx.Where("uid = ? AND comment_type = ? AND comment_oid = ?", target.UID, target.CommentType, target.CommentOID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		item := row.toModel()
		item.BaselineReady = true
		item.LastPollAt = now
		item.LastError = target.LastError
		item.Closed = target.Closed
		item.CommentCount = target.CommentCount
		updated := commentTargetFromModel(item)
		return tx.Save(&updated).Error
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

func (s *Store) ListDeliveries(limit int) ([]model.Delivery, error) {
	var rows []deliveryRow
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

// QueryDeliveries returns deliveries ordered by immutable creation time and id
// so scheduling updates cannot move an item across cursor boundaries.
func (s *Store) QueryDeliveries(query DeliveryQuery) ([]model.Delivery, error) {
	var rows []deliveryRow
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
	var rows []deliveryRow
	q := s.db.Where("state = ? AND next_at <= ?", string(model.DeliveryPending), now.Unix()).Order("next_at ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeDeliveries(rows)
}

func (s *Store) CompleteDelivery(id string) error {
	return s.db.Where("id = ?", id).Delete(&deliveryRow{}).Error
}

// RetryDelivery makes one blocked delivery immediately eligible for the
// background dispatcher without discarding its attempt history or progress.
func (s *Store) RetryDelivery(id string, now time.Time) error {
	result := s.db.Model(&deliveryRow{}).Where("id = ? AND state = ?", id, string(model.DeliveryBlocked)).Updates(map[string]any{
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
	if err := s.db.Model(&deliveryRow{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return ErrDeliveryNotBlocked
}

func (s *Store) FailDelivery(id string, blocked bool, next time.Time, deliveryErr error, progress *model.DeliveryProgress) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row deliveryRow
		if err := tx.Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		d, err := decodeDelivery(row)
		if err != nil {
			return err
		}
		d.Attempts++
		d.NextAt = next
		d.LastError = deliveryErr.Error()
		if progress != nil {
			d.Progress = progress
		}
		if blocked {
			d.State = model.DeliveryBlocked
		}
		return putDeliveryTx(tx, d)
	})
}

func (s *Store) UnblockChannel(channelID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var rows []deliveryRow
		if err := tx.Where("channel_id = ? AND state = ?", channelID, string(model.DeliveryBlocked)).Find(&rows).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		for _, row := range rows {
			d, err := decodeDelivery(row)
			if err != nil {
				return err
			}
			d.State = model.DeliveryPending
			d.NextAt = time.Unix(now, 0)
			if err := putDeliveryTx(tx, d); err != nil {
				return err
			}
		}
		return nil
	})
}

func putDeliveryTx(tx *gorm.DB, d model.Delivery) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encoding delivery: %w", err)
	}
	kind := string(d.EffectiveKind())
	row := deliveryRow{
		ID:          d.ID,
		Kind:        kind,
		ChannelID:   d.ChannelID,
		State:       string(d.State),
		Attempts:    d.Attempts,
		NextAt:      d.NextAt.Unix(),
		LastError:   d.LastError,
		CreatedAt:   d.CreatedAt.Unix(),
		PayloadJSON: string(raw),
	}
	return tx.Save(&row).Error
}

func decodeDeliveries(rows []deliveryRow) ([]model.Delivery, error) {
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

func decodeDelivery(row deliveryRow) (model.Delivery, error) {
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
	if row.Kind != "" {
		d.Kind = model.DeliveryKind(row.Kind)
	}
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
