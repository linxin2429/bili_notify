package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/linxin2429/bili_notify/config"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state/migrations"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	v := mustVault(t, 30)
	store, err := Open(t.Context(), path, v)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store, err = Open(t.Context(), path, v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRefuseLegacyDataDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   []string
		wantErr string
	}{
		{name: "empty", files: nil},
		{name: "data only", files: []string{config.DataFileName}},
		{name: "state only", files: []string{config.LegacyStateFile}, wantErr: "state.db"},
		{name: "content only", files: []string{config.LegacyContentFile}, wantErr: "content.db"},
		{name: "both legacy", files: []string{config.LegacyStateFile, config.LegacyContentFile}, wantErr: "state.db"},
		{name: "data and legacy", files: []string{config.DataFileName, config.LegacyStateFile}, wantErr: "state.db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, name := range tt.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
			}
			err := config.RefuseLegacyDataDir(dir)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "fresh data directory")
		})
	}
}

func TestRunMigrationsCreatesTables(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	v := mustVault(t, 31)
	require.NoError(t, runMigrations(context.Background(), db, v))
	var name string
	require.NoError(t, db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='outbox'`).Scan(&name))
	assert.Equal(t, "outbox", name)
	// second up is no-op
	require.NoError(t, runMigrations(context.Background(), db, v))
}

func TestRuntimeSettingsBooleanMigration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		old  bool
	}{
		{name: "enabled", old: true},
		{name: "disabled", old: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "data.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
			require.NoError(t, err)
			_, err = provider.UpTo(t.Context(), 7)
			require.NoError(t, err)

			oldRecord, err := json.Marshal(map[string]any{
				"_version":        2,
				"comment_enabled": tt.old,
			})
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO meta(key, value) VALUES ('runtime_settings', ?)`, oldRecord)
			require.NoError(t, err)
			_, err = provider.UpTo(t.Context(), 8)
			require.NoError(t, err)

			var brokenType string
			require.NoError(t, db.QueryRow(`SELECT json_type(value, '$.bilibili_comments_enabled') FROM meta WHERE key = 'runtime_settings'`).Scan(&brokenType))
			assert.Equal(t, "integer", brokenType)

			_, err = provider.Up(t.Context())
			require.NoError(t, err)
			var raw string
			require.NoError(t, db.QueryRow(`SELECT value FROM meta WHERE key = 'runtime_settings'`).Scan(&raw))
			var record runtimeSettingsRecord
			require.NoError(t, json.Unmarshal([]byte(raw), &record))
			assert.Equal(t, tt.old, record.BilibiliCommentsEnabled)
		})
	}
}

func TestV10MigrationPreservesV9StateAndRemovesTransitionalTables(t *testing.T) {
	t.Parallel()
	db, path, v := preparePopulatedV9(t)
	require.NoError(t, runMigrations(t.Context(), db, v))
	assertMigrationVersion(t, db, 10)
	for _, table := range []string{"auth_session", "deliveries", "comments", "dynamics", "seen_comments", "seen_dynamics", "comment_targets", "ups"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count))
		assert.Zero(t, count, table)
	}
	require.NoError(t, db.Close())

	store, err := Open(t.Context(), path, v)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	channels, err := store.ListChannels()
	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, "secret", channels[0].Settings["webhook"])
	account, err := store.PlatformAccount(model.PlatformBilibili)
	require.NoError(t, err)
	assert.Equal(t, "cookie-value", account.Session["SESSDATA"])
	assert.Equal(t, "42", account.ExternalID)

	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 4)
	byID := make(map[string]model.Delivery, len(deliveries))
	for _, delivery := range deliveries {
		byID[delivery.ID] = delivery
	}
	assert.Equal(t, model.DeliveryBlocked, byID["content-delivery"].State)
	assert.Equal(t, 3, byID["content-delivery"].Attempts)
	require.NotNil(t, byID["content-delivery"].Progress)
	assert.True(t, byID["content-delivery"].Progress.TextSent)
	assert.Equal(t, model.DeliveryKindComment, byID["comment-delivery"].Kind)
	assert.Equal(t, "bilibili:up:42", byID["comment-delivery"].Comment.UPUID)
	assert.Equal(t, model.DeliveryKindAI, byID["ai-delivery"].Kind)
	assert.Equal(t, "bilibili:up:42", byID["ai-delivery"].AI.SourceID)
	assert.Equal(t, "system", byID["system-delivery"].Dynamic.UID)

	job, err := store.AIJob("ai-job")
	require.NoError(t, err)
	require.NotNil(t, job.Source)
	assert.Equal(t, "bilibili:content:dynamic-1", job.Source.ContentID)
	assert.Equal(t, "bilibili:up:42", job.Source.SourceID)
	assert.Equal(t, "BV1migration", job.Source.BVID)

	var sourceID, title, summary, traceparent string
	require.NoError(t, store.db.Raw(`SELECT source_id,title,summary,origin_traceparent FROM outbox WHERE id='content-delivery'`).Row().Scan(&sourceID, &title, &summary, &traceparent))
	assert.Equal(t, "bilibili:up:42", sourceID)
	assert.Equal(t, "video title", title)
	assert.Equal(t, "body", summary)
	assert.Equal(t, "00-trace", traceparent)
}

func TestV10MigrationRollsBackCorruptV9Payload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{
			name: "channel ciphertext",
			mutate: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(`UPDATE channels SET sealed=x'01' WHERE id='channel'`)
				require.NoError(t, err)
			},
		},
		{
			name: "outbox JSON",
			mutate: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(`UPDATE outbox SET payload_json='{' WHERE id='content-delivery'`)
				require.NoError(t, err)
			},
		},
		{
			name: "AI source snapshot",
			mutate: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(`UPDATE contents SET raw_json='{' WHERE id='bilibili:content:dynamic-1'`)
				require.NoError(t, err)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, _, v := preparePopulatedV9(t)
			tt.mutate(t, db)
			require.Error(t, runMigrations(t.Context(), db, v))
			assertMigrationVersion(t, db, 9)
			var legacyTable, v10Table int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='deliveries'`).Scan(&legacyTable))
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='channels_v10'`).Scan(&v10Table))
			assert.Equal(t, 1, legacyTable)
			assert.Zero(t, v10Table)
		})
	}
}

func preparePopulatedV9(t *testing.T) (*sql.DB, string, *vault.Vault) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`PRAGMA foreign_keys=ON`)
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
	require.NoError(t, err)
	_, err = provider.UpTo(t.Context(), 9)
	require.NoError(t, err)
	v := mustVault(t, 91)
	now := time.Unix(1_700_000_000, 0).UTC()

	channel := model.Channel{ID: "channel", Name: "Robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "secret", "format": "markdown"}, CreatedAt: now, UpdatedAt: now}
	channelSealed, err := sealJSON(v, tableChannels, channel.ID, channel)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO channels(id,sealed) VALUES(?,?)`, channel.ID, channelSealed)
	require.NoError(t, err)
	sessionSealed, err := sealJSON(v, tableAuthSession, authSessionID, model.BiliSession{
		Cookies: map[string]string{"SESSDATA": "cookie-value"}, AccountUID: "42", AccountName: "UP", UpdatedAt: now,
	})
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO platform_accounts(platform,status,sealed_session,sealed_aad,updated_at) VALUES('bilibili','connected',?,'auth_session',?)`, sessionSealed, now.Unix())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sources(id,platform,type,external_id,name,enabled,baseline_state) VALUES('bilibili:up:42','bilibili','up','42','UP',1,'complete')`)
	require.NoError(t, err)
	rawSource := mustMigrationJSON(t, map[string]any{"id": "dynamic-1", "bvid": "BV1migration", "up_name": "UP", "title": "video title", "url": "https://example.com/video"})
	_, err = db.Exec(`INSERT INTO contents(id,platform,source_id,external_id,author_id,author_name,upstream_type,type,title,body_text,url,published_at,first_seen_at,last_synced_at,search_text,raw_json) VALUES('bilibili:content:dynamic-1','bilibili','bilibili:up:42','dynamic-1','42','UP','DYNAMIC_TYPE_AV','video','video title','body','https://example.com/video',?,?,?,?,?)`, now.Unix(), now.Unix(), now.Unix(), "video title body", rawSource)
	require.NoError(t, err)

	profile := model.AIProfile{ID: "profile", Kind: model.AIProfileTranscription, Name: "profile", Enabled: true, Default: true, CreatedAt: now, UpdatedAt: now}
	profileSealed, err := sealJSON(v, tableAIProfiles, profile.ID, sealedAIProfile{AIProfile: profile})
	require.NoError(t, err)
	inputSealed, err := sealJSON(v, tableAIJobInput, "ai-job", model.AITranscriptionInput{BVID: "BV1migration"})
	require.NoError(t, err)
	configSealed, err := sealJSON(v, tableAIJobConfig, "ai-job", sealedAIJobConfig{Profile: sealedAIProfile{AIProfile: profile}})
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ai_profiles(id,kind,name,is_default,sealed,created_at,updated_at,is_enabled) VALUES('profile','transcription','profile',1,?,?,?,1)`, profileSealed, now.Unix(), now.Unix())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ai_jobs(id,client_request_id,kind,state,stage,progress,profile_id,attempts,input_sealed,config_sealed,created_at,updated_at,origin,source_dynamic_id,target_channel_ids) VALUES('ai-job','request','transcription','queued','queued',0,'profile',0,?,?,?,?,'dynamic','bilibili:content:dynamic-1','["channel"]')`, inputSealed, configSealed, now.Unix(), now.Unix())
	require.NoError(t, err)

	content := model.Content{ID: "bilibili:content:dynamic-1", Platform: model.PlatformBilibili, SourceID: "bilibili:up:42", ExternalID: "dynamic-1",
		AuthorID: "42", AuthorName: "UP", UpstreamType: "DYNAMIC_TYPE_AV", Type: model.ContentVideo, Title: "video title", Text: "body",
		URL: "https://example.com/video", PublishedAt: now, FirstSeenAt: now, LastSyncedAt: now}
	digest := model.CommentDigest{Platform: model.PlatformBilibili, SourceID: content.SourceID, ContentID: content.ID, Title: content.Title,
		Triggers: []model.CommentNode{{ID: "bilibili:comment:1", RPID: "1"}}, Paths: []model.CommentPath{{TriggerID: "bilibili:comment:1", Nodes: []model.CommentNode{{ID: "bilibili:comment:1", RPID: "1", Name: "UP", Message: "reply", Time: now}}}}}
	aiNotification := model.AINotification{JobID: "ai-job", DynamicID: content.ID, Title: content.Title, Stage: model.AIJobTranscription, Succeeded: true, Body: "transcript"}
	rows := []struct {
		id, kind, sourceID, contentID, state, payload, progress, trace string
		attempts                                                       int
	}{
		{id: "content-delivery", kind: "content", sourceID: content.SourceID, contentID: content.ID, state: "blocked", attempts: 3, payload: mustMigrationJSON(t, content), progress: `{"text_sent":true}`, trace: "00-trace"},
		{id: "comment-delivery", kind: "comment_digest", sourceID: content.SourceID, contentID: content.ID, state: "pending", payload: mustMigrationJSON(t, digest), progress: `{}`},
		{id: "ai-delivery", kind: "ai", sourceID: content.SourceID, contentID: content.ID, state: "pending", payload: mustMigrationJSON(t, aiNotification), progress: `{}`},
	}
	for _, row := range rows {
		_, err = db.Exec(`INSERT INTO outbox(id,kind,platform,source_id,content_id,channel_id,idempotency_key,state,attempts,next_at,last_error,created_at,payload_json,progress_json,origin_traceparent) VALUES(?,?,?,?,?,'channel',?,?,?,?,?,?,?, ?,?)`,
			row.id, row.kind, "bilibili", row.sourceID, row.contentID, row.id, row.state, row.attempts, now.Add(time.Minute).Unix(), "last error", now.Unix(), row.payload, row.progress, row.trace)
		require.NoError(t, err)
	}
	system := model.Delivery{ID: "system-delivery", Kind: model.DeliveryKindDynamic,
		Dynamic:   model.Dynamic{ID: "system:1", UID: "system", UPName: "Bili Notify", Type: "SYSTEM", Summary: "alert", PublishedAt: now},
		ChannelID: "channel", State: model.DeliveryPending, NextAt: now.Add(time.Minute), CreatedAt: now}
	_, err = db.Exec(`INSERT INTO deliveries(id,kind,channel_id,state,attempts,next_at,last_error,created_at,payload_json) VALUES('system-delivery','dynamic','channel','pending',0,?,'',?,?)`, now.Add(time.Minute).Unix(), now.Unix(), mustMigrationJSON(t, system))
	require.NoError(t, err)
	return db, path, v
}

func mustMigrationJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func assertMigrationVersion(t *testing.T, db *sql.DB, expected int64) {
	t.Helper()
	var version int64
	require.NoError(t, db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version))
	assert.Equal(t, expected, version)
}
