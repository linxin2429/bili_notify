package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionCanceledOperations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		run       func(*Store) error
		operation string
	}{
		{"scan", func(s *Store) error { _, err := s.CollectionScan("42"); return err }, "reading collection scan"},
		{"known", func(s *Store) error { _, err := s.CollectionKnown("42", "one", CollectionScan{}); return err }, "checking collection seen boundary"},
		{"stage", func(s *Store) error { return s.StageCollectionPage(nil, nil) }, "staging collection page"},
		{"feed", func(s *Store) error { return s.StageFeedPage("100", nil, nil) }, "staging aggregate feed page"},
		{"due", func(s *Store) error { _, err := s.DueCollectionItems("42", "dynamic", time.Now(), 10); return err }, "querying due collection items"},
		{"pending", func(s *Store) error { return s.CollectionPendingError("42") }, "reading pending collection"},
		{"complete", func(s *Store) error { return s.CompleteCollectionItem(CollectionItem{}) }, "completing collection item"},
		{"fail", func(s *Store) error { return s.FailCollectionItem(CollectionItem{}, time.Now(), errors.New("retry")) }, "deferring collection item"},
		{"AI retry", func(s *Store) error { return s.RetryAutomaticAI(time.Now()) }, "querying automatic AI recovery intents"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 33)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := tt.run(store.WithContext(ctx))
			require.ErrorIs(t, err, context.Canceled)
			assert.Contains(t, err.Error(), tt.operation)
			rows, err := store.DueCollectionItems("", "dynamic", time.Now(), 100)
			require.NoError(t, err)
			assert.Empty(t, rows)
		})
	}
}

func TestCollectionRejectsMismatchedIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CollectionItem)
	}{
		{"kind", func(i *CollectionItem) { i.Kind = "ai" }},
		{"id", func(i *CollectionItem) { i.ID = "other" }},
		{"source", func(i *CollectionItem) { i.SourceID = "bilibili:up:99" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 34)
			require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
			item := CollectionItem{SourceID: "bilibili:up:42", ID: "one", Kind: "dynamic", Payload: []byte(`{}`)}
			require.NoError(t, store.StageCollectionPage([]CollectionItem{item}, nil))
			tt.mutate(&item)
			count, err := store.RecordCollectedDynamic(item, model.Dynamic{ID: "one", UID: "42", Type: "DYNAMIC_TYPE_WORD"})
			require.ErrorContains(t, err, "identity")
			assert.Zero(t, count)
			rows, err := store.DueCollectionItems("42", "dynamic", time.Now(), 10)
			require.NoError(t, err)
			assert.Len(t, rows, 1)
		})
	}
}

func TestCollectionTransactionsRetainWorkOnWriteFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, trigger string
		feed          bool
	}{
		{name: "content insert", trigger: `CREATE TRIGGER reject_content BEFORE INSERT ON contents BEGIN SELECT RAISE(ABORT, 'test write failure'); END`},
		{name: "retry deletion", trigger: `CREATE TRIGGER reject_retry_delete BEFORE DELETE ON collection_items BEGIN SELECT RAISE(ABORT, 'test write failure'); END`},
		{name: "feed gap insert", feed: true, trigger: `CREATE TRIGGER reject_gap BEFORE INSERT ON collection_feed_gaps BEGIN SELECT RAISE(ABORT, 'test write failure'); END`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 35)
			require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
			item := CollectionItem{SourceID: "bilibili:up:42", ID: "one", Kind: "dynamic", Payload: []byte(`{}`)}
			require.NoError(t, store.db.Exec(tt.trigger).Error)
			if tt.feed {
				require.ErrorContains(t, store.StageFeedPage("100", []CollectionItem{item}, []json.RawMessage{json.RawMessage(`{"id":"gap"}`)}), "test write failure")
			} else {
				require.NoError(t, store.StageCollectionPage([]CollectionItem{item}, nil))
				_, err := store.RecordCollectedDynamic(item, model.Dynamic{ID: "one", UID: "42", Type: "DYNAMIC_TYPE_WORD", PublishedAt: time.Now()})
				require.ErrorContains(t, err, "test write failure")
			}
			seen, err := store.Seen("42", "one")
			require.NoError(t, err)
			assert.False(t, seen)
			rows, err := store.DueCollectionItems("42", "dynamic", time.Now(), 10)
			require.NoError(t, err)
			if tt.feed {
				assert.Empty(t, rows)
			} else {
				assert.Len(t, rows, 1)
			}
		})
	}
}

func TestAutomaticAIRetryRetainsMalformedIntent(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 36)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	item := CollectionItem{SourceID: "bilibili:up:42", ID: "one", Kind: "ai", Payload: []byte(`"invalid intent"`)}
	require.NoError(t, store.StageCollectionPage([]CollectionItem{item}, nil))
	at := time.Now()
	err := store.RetryAutomaticAI(at)
	var typeErr *json.UnmarshalTypeError
	require.ErrorAs(t, err, &typeErr)
	rows, err := store.DueCollectionItems("42", "ai", at, 10)
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = store.DueCollectionItems("42", "ai", at.Add(time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 1, rows[0].Attempts)
	assert.Equal(t, item.Payload, rows[0].Payload)
}
