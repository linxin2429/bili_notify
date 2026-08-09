package state

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state/migrations"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordingTransactionsRollBackEveryObservableState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "dynamic outbox failure", run: testDynamicOutboxRollback},
		{name: "dynamic baseline failure", run: testDynamicBaselineRollback},
		{name: "feed outbox failure", run: testFeedOutboxRollback},
		{name: "feed cursor failure", run: testFeedCursorRollback},
		{name: "comment outbox failure", run: testCommentOutboxRollback},
		{name: "comment target failure", run: testCommentTargetRollback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

func testDynamicOutboxRollback(t *testing.T) {
	store := openTestStore(t, 61)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	installFailureTrigger(t, store, "deliveries", "INSERT", "forced delivery failure")
	dynamic := resilienceDynamic("dynamic-outbox", "42")
	created, err := store.RecordDynamics("42", []model.Dynamic{dynamic}, []string{"channel"}, DynamicBaselineNone)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forced delivery failure")
	assert.Equal(t, 0, created)
	assertRecordingState(t, store, "42", dynamic.ID, 0, 0, false)
}

func testDynamicBaselineRollback(t *testing.T) {
	store := openTestStore(t, 62)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	installFailureTrigger(t, store, "ups", "UPDATE", "forced baseline failure")
	dynamic := resilienceDynamic("dynamic-baseline", "42")
	created, err := store.RecordDynamics("42", []model.Dynamic{dynamic}, nil, DynamicBaselineAll)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forced baseline failure")
	assert.Equal(t, 0, created)
	assertRecordingState(t, store, "42", dynamic.ID, 0, 0, false)
	up, upErr := store.UP("42")
	require.NoError(t, upErr)
	assert.False(t, up.BaselineReady)
	assert.False(t, up.ExclusiveBaselineReady)
}

func testFeedOutboxRollback(t *testing.T) {
	store := openTestStore(t, 63)
	require.NoError(t, store.InitializeFeed("account", "old", time.Now()))
	installFailureTrigger(t, store, "deliveries", "INSERT", "forced feed delivery failure")
	dynamic := resilienceDynamic("feed-outbox", "42")
	created, err := store.RecordFeedDynamics("account", "new", []model.Dynamic{dynamic}, []string{"channel"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forced feed delivery failure")
	assert.Equal(t, 0, created)
	assertRecordingState(t, store, "42", dynamic.ID, 0, 0, false)
	assertFeedBaseline(t, store, "account", "old")
}

func testFeedCursorRollback(t *testing.T) {
	store := openTestStore(t, 64)
	require.NoError(t, store.InitializeFeed("account", "old", time.Now()))
	installFailureTrigger(t, store, "bili_feed_state", "UPDATE", "forced cursor failure")
	dynamic := resilienceDynamic("feed-cursor", "42")
	created, err := store.RecordFeedDynamics("account", "new", []model.Dynamic{dynamic}, []string{"channel"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forced cursor failure")
	assert.Equal(t, 0, created)
	assertRecordingState(t, store, "42", dynamic.ID, 0, 0, false)
	assertFeedBaseline(t, store, "account", "old")
}

func testCommentOutboxRollback(t *testing.T) {
	store, target, note := prepareCommentRollback(t, 65, "comment-outbox")
	installFailureTrigger(t, store, "deliveries", "INSERT", "forced comment delivery failure")
	created, err := store.RecordCommentNotifications(target, []model.CommentNotification{note}, []string{"channel"}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forced comment delivery failure")
	assert.Equal(t, 0, created)
	assertCommentRecordingState(t, store, target, note.RPID)
}

func testCommentTargetRollback(t *testing.T) {
	store, target, note := prepareCommentRollback(t, 66, "comment-target")
	installFailureTrigger(t, store, "comment_targets", "UPDATE", "forced target failure")
	created, err := store.RecordCommentNotifications(target, []model.CommentNotification{note}, []string{"channel"}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forced target failure")
	assert.Equal(t, 0, created)
	assertCommentRecordingState(t, store, target, note.RPID)
}

func prepareCommentRollback(t *testing.T, fill byte, rpid string) (*Store, model.CommentTarget, model.CommentNotification) {
	t.Helper()
	store := openTestStore(t, fill)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	target := model.CommentTarget{UID: "42", CommentType: 17, CommentOID: "oid", PublishedAt: time.Now()}
	require.NoError(t, store.PutCommentTargets("42", []model.CommentTarget{target}))
	note := model.CommentNotification{RPID: rpid, UPUID: "42", UPName: "up", PublishedAt: time.Now(), Thread: []model.CommentNode{{RPID: rpid, Message: "reply", IsUP: true}}}
	return store, target, note
}

func installFailureTrigger(t *testing.T, store *Store, table, operation, message string) {
	t.Helper()
	statement := fmt.Sprintf(`CREATE TRIGGER deterministic_failure BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, '%s'); END`, operation, table, message)
	require.NoError(t, store.db.Exec(statement).Error)
}

func assertRecordingState(t *testing.T, store *Store, uid, dynamicID string, dynamics, deliveries int, seen bool) {
	t.Helper()
	assertTableCount(t, store, "dynamics", dynamics)
	assertTableCount(t, store, "deliveries", deliveries)
	gotSeen, err := store.Seen(uid, dynamicID)
	require.NoError(t, err)
	assert.Equal(t, seen, gotSeen)
}

func assertCommentRecordingState(t *testing.T, store *Store, target model.CommentTarget, rpid string) {
	t.Helper()
	assertTableCount(t, store, "comments", 0)
	assertTableCount(t, store, "deliveries", 0)
	seen, err := store.CommentSeen(target.UID, rpid)
	require.NoError(t, err)
	assert.False(t, seen)
	targets, err := store.ListCommentTargets(target.UID)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.False(t, targets[0].BaselineReady)
	assert.True(t, targets[0].LastPollAt.IsZero())
}

func assertFeedBaseline(t *testing.T, store *Store, accountUID, want string) {
	t.Helper()
	feed, err := store.FeedState(accountUID)
	require.NoError(t, err)
	assert.Equal(t, want, feed.UpdateBaseline)
}

func assertTableCount(t *testing.T, store *Store, table string, want int) {
	t.Helper()
	var got int64
	require.NoError(t, store.db.Table(table).Count(&got).Error)
	assert.Equal(t, int64(want), got, "table %s", table)
}

func TestConcurrentRecordingCreatesOneLogicalResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, *Store, []string) (int, error)
		seen func(*testing.T, *Store) bool
		kind model.DeliveryKind
	}{
		{
			name: "same dynamic",
			run: func(t *testing.T, store *Store, channels []string) (int, error) {
				t.Helper()
				return store.RecordDynamics("42", []model.Dynamic{resilienceDynamic("concurrent-dynamic", "42")}, channels, DynamicBaselineNone)
			},
			seen: func(t *testing.T, store *Store) bool {
				t.Helper()
				seen, err := store.Seen("42", "concurrent-dynamic")
				require.NoError(t, err)
				return seen
			},
			kind: model.DeliveryKindDynamic,
		},
		{
			name: "same reply",
			run: func(t *testing.T, store *Store, channels []string) (int, error) {
				t.Helper()
				target := model.CommentTarget{UID: "42", CommentType: 17, CommentOID: "oid"}
				note := model.CommentNotification{RPID: "concurrent-comment", UPUID: "42", PublishedAt: time.Now()}
				return store.RecordCommentNotifications(target, []model.CommentNotification{note}, channels, false)
			},
			seen: func(t *testing.T, store *Store) bool {
				t.Helper()
				seen, err := store.CommentSeen("42", "concurrent-comment")
				require.NoError(t, err)
				return seen
			},
			kind: model.DeliveryKindComment,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 67)
			const workers = 12
			channels := []string{"channel-a", "channel-b", "channel-a"}
			start := make(chan struct{})
			results := make(chan int, workers)
			errs := make(chan error, workers)
			var ready sync.WaitGroup
			ready.Add(workers)
			var done sync.WaitGroup
			done.Add(workers)
			for range workers {
				go func() {
					ready.Done()
					<-start
					created, err := tt.run(t, store, channels)
					results <- created
					errs <- err
					done.Done()
				}()
			}
			ready.Wait()
			close(start)
			done.Wait()
			close(results)
			close(errs)

			totalCreated := 0
			for err := range errs {
				require.NoError(t, err)
			}
			for created := range results {
				totalCreated += created
			}
			assert.Equal(t, 1, totalCreated)
			assert.True(t, tt.seen(t, store))
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			require.Len(t, deliveries, 2)
			ids := map[string]struct{}{}
			for _, delivery := range deliveries {
				assert.Equal(t, tt.kind, delivery.EffectiveKind())
				ids[delivery.ID] = struct{}{}
			}
			assert.Len(t, ids, 2)
		})
	}
}

func TestMigrationMatrixPreservesHistoricalDataAndConstraints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version int64
	}{
		{name: "schema v1", version: 1},
		{name: "schema v2", version: 2},
		{name: "schema v3", version: 3},
		{name: "schema v4", version: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "data.db")
			v := mustVault(t, byte(70+tt.version))
			db, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
			require.NoError(t, err)
			_, err = provider.UpTo(t.Context(), tt.version)
			require.NoError(t, err)
			seedHistoricalSchema(t, db, v, tt.version)
			require.NoError(t, db.Close())

			store, err := Open(t.Context(), path, v)
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			up, err := store.UP("42")
			require.NoError(t, err)
			assert.True(t, up.BaselineReady)
			assert.Equal(t, tt.version >= 2, up.ExclusiveBaselineReady)
			channels, err := store.ListChannels()
			require.NoError(t, err)
			require.Len(t, channels, 1)
			assert.Equal(t, "secret", channels[0].Settings["token"])
			seen, err := store.Seen("42", "historical")
			require.NoError(t, err)
			assert.True(t, seen)
			_, total, err := store.QueryDynamics(ContentQuery{UID: "42"})
			require.NoError(t, err)
			assert.Equal(t, 1, total)
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			require.Len(t, deliveries, 1)
			assert.Equal(t, "historical", deliveries[0].Dynamic.ID)
			if tt.version >= 3 {
				assertFeedBaseline(t, store, "account", "cursor")
				relations, relationErr := store.FollowRelations("account")
				require.NoError(t, relationErr)
				assert.Equal(t, model.Followed, relations["42"].State)
			}
			if tt.version >= 4 {
				assertTableCount(t, store, "audit_logs", 1)
			}
			for _, index := range []string{"idx_deliveries_due", "idx_dyn_uid_pub", "idx_up_follow_relations_up", "idx_audit_logs_action_time"} {
				var count int
				require.NoError(t, store.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count).Error)
				assert.Equal(t, 1, count, "index %s", index)
			}
			duplicate := store.db.Create(&seenDynamicRow{UID: "42", DynamicID: "historical", SeenAt: time.Now().Unix()}).Error
			require.Error(t, duplicate)
			assert.Contains(t, duplicate.Error(), "UNIQUE constraint failed")
		})
	}
}

func seedHistoricalSchema(t *testing.T, db *sql.DB, v *vault.Vault, version int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('admin_password_hash', 'hash')`)
	require.NoError(t, err)
	if version == 1 {
		_, err = db.Exec(`INSERT INTO ups (uid, enabled, baseline_ready) VALUES ('42', 1, 1)`)
	} else {
		_, err = db.Exec(`INSERT INTO ups (uid, enabled, baseline_ready, exclusive_baseline_ready) VALUES ('42', 1, 1, 1)`)
	}
	require.NoError(t, err)
	channel := model.Channel{ID: "channel", Name: "historical", Type: model.ChannelFeishu, Settings: map[string]string{"token": "secret"}}
	sealed, err := sealJSON(v, tableChannels, channel.ID, channel)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO channels (id, sealed) VALUES (?, ?)`, channel.ID, sealed)
	require.NoError(t, err)
	published := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	dynamic := resilienceDynamic("historical", "42")
	dynamic.PublishedAt = published
	payload, err := json.Marshal(dynamic)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO dynamics
		(id, uid, up_name, type, published_at, discovered_at, baseline, search_text, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, 0, 'historical', ?)`, dynamic.ID, dynamic.UID, dynamic.UPName, dynamic.Type, published.Unix(), published.Unix(), payload)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO seen_dynamics (uid, dynamic_id, seen_at) VALUES ('42', 'historical', ?)`, published.Unix())
	require.NoError(t, err)
	delivery := model.Delivery{ID: "historical:channel", Kind: model.DeliveryKindDynamic, Dynamic: dynamic, ChannelID: "channel", State: model.DeliveryPending, NextAt: published, CreatedAt: published}
	deliveryPayload, err := json.Marshal(delivery)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO deliveries (id, kind, channel_id, state, next_at, created_at, payload_json)
		VALUES (?, 'dynamic', 'channel', 'pending', ?, ?, ?)`, delivery.ID, published.Unix(), published.Unix(), deliveryPayload)
	require.NoError(t, err)
	if version >= 3 {
		_, err = db.Exec(`INSERT INTO bili_feed_state (account_uid, update_baseline, initialized, updated_at) VALUES ('account', 'cursor', 1, ?)`, published.Unix())
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO up_follow_relations (account_uid, up_uid, follow_state, space_synced) VALUES ('account', '42', 'followed', 1)`)
		require.NoError(t, err)
	}
	if version >= 4 {
		_, err = db.Exec(`INSERT INTO audit_logs
			(occurred_at, request_id, actor, action, outcome, http_method, route, status_code, duration_ms)
			VALUES (?, 'request', 'admin', 'up.create', 'success', 'POST', '/api/v2/ups', 201, 1)`, published.Unix())
		require.NoError(t, err)
	}
}

func TestMigrationRejectsUntrustworthySchemaState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
		want  string
	}{
		{
			name: "future migration version",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
				require.NoError(t, err)
				_, err = provider.Up(t.Context())
				require.NoError(t, err)
				_, err = db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (999, 1)`)
				require.NoError(t, err)
			},
			want: "999",
		},
		{
			name: "corrupt goose metadata",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY, version_id INTEGER)`)
				require.NoError(t, err)
			},
			want: "goose_db_version",
		},
		{
			name: "incompatible application schema",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`CREATE TABLE ups (uid TEXT PRIMARY KEY)`)
				require.NoError(t, err)
			},
			want: "ups",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "data.db")
			db, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			tt.setup(t, db)
			require.NoError(t, db.Close())
			store, err := Open(t.Context(), path, mustVault(t, 80))
			if store != nil {
				t.Cleanup(func() { _ = store.Close() })
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Contains(t, err.Error(), "migration")
		})
	}
}

func TestEncryptedStateRejectsWrongKeysAndTamperingAfterReopen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store)
		vault  func(*testing.T) *vault.Vault
		read   func(*Store) error
		want   string
	}{
		{
			name:   "wrong master key",
			mutate: func(*testing.T, *Store) {},
			vault:  func(t *testing.T) *vault.Vault { return mustVault(t, 82) },
			read: func(store *Store) error {
				_, err := store.ListChannels()
				return err
			},
			want: "authentication failed",
		},
		{
			name: "tampered channel ciphertext",
			mutate: func(t *testing.T, store *Store) {
				t.Helper()
				require.NoError(t, store.db.Exec(`UPDATE channels SET sealed = X'010000' WHERE id = 'channel'`).Error)
			},
			vault: func(t *testing.T) *vault.Vault { return mustVault(t, 81) },
			read: func(store *Store) error {
				_, err := store.Channel("channel")
				return err
			},
			want: "invalid encrypted value nonce",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "data.db")
			correctVault := mustVault(t, 81)
			store, err := Open(t.Context(), path, correctVault)
			require.NoError(t, err)
			_, err = store.PutChannel(model.Channel{ID: "channel", Name: "secret", Type: model.ChannelWeCom, Settings: map[string]string{"webhook": "https://example.com/hook"}})
			require.NoError(t, err)
			tt.mutate(t, store)
			require.NoError(t, store.Close())

			reopened, err := Open(t.Context(), path, tt.vault(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = reopened.Close() })
			err = tt.read(reopened)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestOpenRejectsCorruptSQLiteFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
	}{
		{name: "random bytes", data: bytes.Repeat([]byte{0xa5}, 4096)},
		{name: "truncated sqlite header", data: []byte("SQLite format 3\x00")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "data.db")
			require.NoError(t, os.WriteFile(path, tt.data, 0o600))
			store, err := Open(t.Context(), path, mustVault(t, 83))
			if store != nil {
				t.Cleanup(func() { _ = store.Close() })
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "database")
		})
	}
}

func TestClosedDatabaseBackupRestoresDatabaseAndKeyAsOneUnit(t *testing.T) {
	t.Parallel()
	sourceDir := t.TempDir()
	backupDir := t.TempDir()
	key := bytes.Repeat([]byte{84}, 32)
	v, err := vault.New(key)
	require.NoError(t, err)
	sourcePath := filepath.Join(sourceDir, "data.db")
	store, err := Open(t.Context(), sourcePath, v)
	require.NoError(t, err)
	channel, err := store.PutChannel(model.Channel{Name: "backup", Type: model.ChannelWeCom, Settings: map[string]string{"webhook": "https://example.com/hook"}})
	require.NoError(t, err)
	_, err = store.RecordDynamics("42", []model.Dynamic{resilienceDynamic("backup-dynamic", "42")}, []string{channel.ID}, DynamicBaselineNone)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, copyTestFile(sourcePath, filepath.Join(backupDir, "data.db")))
	require.NoError(t, os.WriteFile(filepath.Join(backupDir, "master.key"), key, 0o600))

	restoredKey, err := os.ReadFile(filepath.Join(backupDir, "master.key"))
	require.NoError(t, err)
	restoredVault, err := vault.New(restoredKey)
	require.NoError(t, err)
	restored, err := Open(t.Context(), filepath.Join(backupDir, "data.db"), restoredVault)
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })
	channels, err := restored.ListChannels()
	require.NoError(t, err)
	require.Len(t, channels, 1)
	assert.Equal(t, "https://example.com/hook", channels[0].Settings["webhook"])
	deliveries, err := restored.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, "backup-dynamic", deliveries[0].Dynamic.ID)
}

func copyTestFile(source, destination string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, raw, 0o600)
}

const crashRecoveryChild = "BILI_NOTIFY_CRASH_RECOVERY_CHILD"

func TestCommittedWALStateSurvivesAbruptProcessDeath(t *testing.T) {
	if os.Getenv(crashRecoveryChild) == "1" {
		runCrashRecoveryChild(t)
		return
	}
	path := filepath.Join(t.TempDir(), "data.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedWALStateSurvivesAbruptProcessDeath$")
	cmd.Env = append(os.Environ(), crashRecoveryChild+"=1", "BILI_NOTIFY_CRASH_RECOVERY_DB="+path)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = stdin.Close() })
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "committed\n", line)
	wInfo, err := os.Stat(path + "-wal")
	require.NoError(t, err)
	assert.Positive(t, wInfo.Size())
	require.NoError(t, cmd.Process.Kill())
	processState, err := cmd.Process.Wait()
	require.NoError(t, err)
	assert.False(t, processState.Success())
	cmd.Process = nil

	store, err := Open(t.Context(), path, mustVault(t, 85))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	seen, err := store.Seen("42", "wal-dynamic")
	require.NoError(t, err)
	assert.True(t, seen)
	_, total, err := store.QueryDynamics(ContentQuery{UID: "42"})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	assert.Equal(t, "wal-dynamic", deliveries[0].Dynamic.ID)
}

func runCrashRecoveryChild(t *testing.T) {
	t.Helper()
	path := os.Getenv("BILI_NOTIFY_CRASH_RECOVERY_DB")
	require.NotEmpty(t, path)
	store, err := Open(t.Context(), path, mustVault(t, 85))
	require.NoError(t, err)
	require.NoError(t, store.db.Exec(`PRAGMA wal_autocheckpoint = 0`).Error)
	_, err = store.RecordDynamics("42", []model.Dynamic{resilienceDynamic("wal-dynamic", "42")}, []string{"channel"}, DynamicBaselineNone)
	require.NoError(t, err)
	_, err = fmt.Fprintln(os.Stdout, "committed")
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, os.Stdin)
	require.NoError(t, err)
}

func TestResourceDeletionAndRetryDoNotLeaveOrphans(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "deleting UP cancels only its pending work", run: testDeleteUPCleansOutbox},
		{name: "corrupt outbox aborts UP deletion atomically", run: testDeleteUPRejectsCorruptOutbox},
		{name: "channel deletion is blocked until outbox is empty", run: testDeleteChannelRequiresEmptyOutbox},
		{name: "retry state survives reopen", run: testRetrySurvivesReopen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

func testDeleteUPCleansOutbox(t *testing.T) {
	store := openTestStore(t, 86)
	for _, uid := range []string{"42", "43"} {
		require.NoError(t, store.PutUP(model.UP{UID: uid, Enabled: true}))
		_, err := store.RecordDynamics(uid, []model.Dynamic{resilienceDynamic("dynamic-"+uid, uid)}, []string{"channel"}, DynamicBaselineNone)
		require.NoError(t, err)
		target := model.CommentTarget{UID: uid, CommentType: 17, CommentOID: "oid-" + uid}
		require.NoError(t, store.PutCommentTargets(uid, []model.CommentTarget{target}))
		_, err = store.RecordCommentNotifications(target, []model.CommentNotification{{RPID: "comment-" + uid, UPUID: uid, PublishedAt: time.Now()}}, []string{"channel"}, false)
		require.NoError(t, err)
	}
	require.NoError(t, store.DeleteUP("42"))
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 2)
	for _, delivery := range deliveries {
		if delivery.EffectiveKind() == model.DeliveryKindDynamic {
			assert.Equal(t, "43", delivery.Dynamic.UID)
		} else {
			require.NotNil(t, delivery.Comment)
			assert.Equal(t, "43", delivery.Comment.UPUID)
		}
	}
	_, err = store.GetDynamic("dynamic-42")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetComment("comment-42")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetDynamic("dynamic-43")
	require.NoError(t, err)
}

func testDeleteUPRejectsCorruptOutbox(t *testing.T) {
	store := openTestStore(t, 89)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	_, err := store.RecordDynamics("42", []model.Dynamic{resilienceDynamic("corrupt-delete", "42")}, []string{"channel"}, DynamicBaselineNone)
	require.NoError(t, err)
	require.NoError(t, store.db.Model(&deliveryRow{}).Where("id = ?", "corrupt-delete:channel").Update("payload_json", "{").Error)
	err = store.DeleteUP("42")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding delivery")
	_, err = store.UP("42")
	require.NoError(t, err)
	seen, err := store.Seen("42", "corrupt-delete")
	require.NoError(t, err)
	assert.True(t, seen)
	_, err = store.GetDynamic("corrupt-delete")
	require.NoError(t, err)
	assertTableCount(t, store, "deliveries", 1)
}

func testDeleteChannelRequiresEmptyOutbox(t *testing.T) {
	store := openTestStore(t, 87)
	channel, err := store.PutChannel(model.Channel{Name: "channel", Type: model.ChannelWeCom, Settings: map[string]string{"webhook": "https://example.com/hook"}})
	require.NoError(t, err)
	_, err = store.RecordDynamics("42", []model.Dynamic{resilienceDynamic("channel-dynamic", "42")}, []string{channel.ID}, DynamicBaselineNone)
	require.NoError(t, err)
	err = store.DeleteChannel(channel.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pending deliveries")
	_, err = store.Channel(channel.ID)
	require.NoError(t, err)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.NoError(t, store.CompleteDelivery(deliveries[0].ID))
	require.NoError(t, store.DeleteChannel(channel.ID))
	_, err = store.Channel(channel.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func testRetrySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	v := mustVault(t, 88)
	store, err := Open(t.Context(), path, v)
	require.NoError(t, err)
	_, err = store.RecordDynamics("42", []model.Dynamic{resilienceDynamic("retry-dynamic", "42")}, []string{"channel"}, DynamicBaselineNone)
	require.NoError(t, err)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	progress := &model.DeliveryProgress{TextSent: true, ImagesSent: 2}
	require.NoError(t, store.FailDelivery(deliveries[0].ID, true, time.Now().Add(time.Hour), errors.New("blocked"), progress))
	require.NoError(t, store.Close())

	store, err = Open(t.Context(), path, v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	retryAt := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.RetryDelivery(deliveries[0].ID, retryAt))
	retried, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, retried, 1)
	assert.Equal(t, model.DeliveryPending, retried[0].State)
	assert.Equal(t, 1, retried[0].Attempts)
	assert.Equal(t, "blocked", retried[0].LastError)
	assert.Equal(t, progress, retried[0].Progress)
	assert.True(t, retryAt.Equal(retried[0].NextAt))
}

func resilienceDynamic(id, uid string) model.Dynamic {
	return model.Dynamic{ID: id, UID: uid, UPName: "up", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now(), Summary: "body"}
}
