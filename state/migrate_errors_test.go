package state

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationsHonorCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(context.Context, *sql.Tx, *vault.Vault) error
	}{
		{"v10", func(ctx context.Context, tx *sql.Tx, v *vault.Vault) error { return migrateV10(v)(ctx, tx) }},
		{"accounts", migrateV10Accounts}, {"channels", migrateV10Channels},
		{"sync targets", func(ctx context.Context, tx *sql.Tx, _ *vault.Vault) error { return migrateV10SyncTargets(ctx, tx) }},
		{"outbox", func(ctx context.Context, tx *sql.Tx, _ *vault.Vault) error { return migrateV10Outbox(ctx, tx) }},
		{"AI jobs", func(ctx context.Context, tx *sql.Tx, _ *vault.Vault) error { return migrateV10AIJobs(ctx, tx) }},
		{"v11", func(ctx context.Context, tx *sql.Tx, v *vault.Vault) error { return migrateV11(v)(ctx, tx) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, _, v := preparePopulatedV9(t)
			tx, err := db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, tt.run(ctx, tx, v), context.Canceled)
			require.NoError(t, tx.Rollback())
			assertMigrationVersion(t, db, 9)
		})
	}
}

func TestLegacyDeliveryNormalizationIsIdempotent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		delivery        model.Delivery
		source, content string
	}{
		{name: "dynamic", delivery: model.Delivery{Kind: model.DeliveryKindDynamic, Dynamic: model.Dynamic{UID: "42", ID: "one"}}, source: "bilibili:up:42", content: "bilibili:content:one"},
		{name: "system", delivery: model.Delivery{Kind: model.DeliveryKindDynamic, Dynamic: model.Dynamic{UID: "system", ID: "alert"}}, source: "system", content: "alert"},
		{name: "comment", delivery: model.Delivery{Kind: model.DeliveryKindComment, Comment: &model.CommentNotification{UPUID: "42", ContentID: "one"}}, source: "bilibili:up:42", content: "bilibili:content:one"},
		{name: "missing comment", delivery: model.Delivery{Kind: model.DeliveryKindComment}},
		{name: "AI", delivery: model.Delivery{Kind: model.DeliveryKindAI, AI: &model.AINotification{DynamicID: "one"}}, content: "bilibili:content:one"},
		{name: "missing AI", delivery: model.Delivery{Kind: model.DeliveryKindAI}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			delivery := tt.delivery
			normalizeLegacyDelivery(&delivery)
			for range 2 {
				normalizeLegacyDelivery(&delivery)
				switch delivery.Kind {
				case model.DeliveryKindDynamic:
					assert.Equal(t, tt.source, delivery.Dynamic.UID)
					assert.Equal(t, tt.content, delivery.Dynamic.ID)
				case model.DeliveryKindComment:
					if delivery.Comment != nil {
						assert.Equal(t, tt.source, delivery.Comment.UPUID)
						assert.Equal(t, tt.content, delivery.Comment.ContentID)
					} else {
						assert.Nil(t, tt.delivery.Comment)
					}
				case model.DeliveryKindAI:
					if delivery.AI != nil {
						assert.Equal(t, tt.content, delivery.AI.DynamicID)
					} else {
						assert.Nil(t, tt.delivery.AI)
					}
				}
			}
		})
	}
}

func TestMigrationDeliverySnapshotValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*model.Delivery)
		want   string
	}{
		{"missing scheduling id", func(d *model.Delivery) { d.ID = "" }, "scheduling"},
		{"missing content id", func(d *model.Delivery) { d.Dynamic.ID = "" }, "content snapshot"},
		{"missing comment", func(d *model.Delivery) { d.Kind = model.DeliveryKindComment }, "comment snapshot"},
		{"missing AI", func(d *model.Delivery) { d.Kind = model.DeliveryKindAI }, "AI snapshot"},
		{"unsupported kind", func(d *model.Delivery) { d.Kind = "unknown" }, "unsupported delivery"},
		{"legacy platform inferred", func(d *model.Delivery) { d.Dynamic.Platform = "" }, ""},
		{"comment platform inferred", func(d *model.Delivery) {
			d.Kind = model.DeliveryKindComment
			d.Comment = &model.CommentNotification{RPID: "r", UPUID: "source", ContentID: "content"}
		}, ""},
		{"AI source preserved", func(d *model.Delivery) {
			d.Kind = model.DeliveryKindAI
			d.AI = &model.AINotification{JobID: "job", SourceID: "source", DynamicID: "content"}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			delivery := model.Delivery{ID: "delivery", ChannelID: "channel", State: model.DeliveryPending, NextAt: time.Now(), CreatedAt: time.Now(), Kind: model.DeliveryKindDynamic, Dynamic: model.Dynamic{ID: "content", UID: "source", Platform: model.PlatformBilibili}}
			tt.mutate(&delivery)
			platform, source, content, err := validateDeliverySnapshot(delivery)
			if tt.want != "" {
				require.ErrorContains(t, err, tt.want)
				assert.Empty(t, platform)
				assert.Empty(t, source)
				assert.Empty(t, content)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "bilibili", platform)
			assert.Equal(t, "source", source)
			assert.Equal(t, "content", content)
		})
	}
}

func TestV10LegacyAINotificationsAndDuplicateIdentities(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, id, channel, content, want string
		count                            int
	}{
		{name: "legacy source resolved", id: "legacy-ai", channel: "channel", content: "dynamic-1", count: 5},
		{name: "same identity deduplicated", id: "ai-delivery", channel: "channel", content: "dynamic-1", count: 4},
		{name: "conflicting channel", id: "ai-delivery", channel: "other", content: "dynamic-1", want: "conflicting outbox identity"},
		{name: "missing content", id: "legacy-ai", channel: "channel", content: "missing", want: "loading AI delivery"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, _, v := preparePopulatedV9(t)
			delivery := model.Delivery{Kind: model.DeliveryKindAI, AI: &model.AINotification{JobID: "ai-job", DynamicID: tt.content, Body: "transcript"}}
			_, err := db.Exec(`INSERT INTO deliveries(id,kind,channel_id,state,attempts,next_at,last_error,created_at,payload_json) VALUES(?,'ai',?,'pending',0,1700000060,'',1700000000,?)`, tt.id, tt.channel, mustMigrationJSON(t, delivery))
			require.NoError(t, err)
			err = runMigrations(t.Context(), db, v)
			if tt.want != "" {
				require.ErrorContains(t, err, tt.want)
				assertMigrationVersion(t, db, 9)
				return
			}
			require.NoError(t, err)
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&count))
			assert.Equal(t, tt.count, count)
			var source, content string
			require.NoError(t, db.QueryRow(`SELECT source_id,content_id FROM outbox WHERE id=?`, tt.id).Scan(&source, &content))
			assert.Equal(t, "bilibili:up:42", source)
			assert.Equal(t, "bilibili:content:dynamic-1", content)
		})
	}
}
