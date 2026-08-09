package state

import (
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAIJobLifecycleAndEncryptedDetail(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 111)
	profile, err := store.PutAIProfile(model.AIProfile{Name: "transcriber", Kind: model.AIProfileTranscription, BaseURL: "https://openrouter.ai/api/v1", Model: "openai/gpt-transcribe", APIKey: "secret", Language: "zh", TimeoutSec: 600, Default: true})
	require.NoError(t, err)
	prompt, err := store.PutAIPrompt(model.AIPromptTemplate{Name: "default", ChunkPrompt: "chunk {{text}}", ReducePrompt: "reduce {{summaries}}", Default: true})
	require.NoError(t, err)

	job, created, err := store.CreateAIJob(model.AIJob{ClientRequestID: "request-1", Kind: model.AIJobTranscription, ProfileID: profile.ID, Input: model.AITranscriptionInput{BVID: "BV1xx411c7mD"}})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, model.AIJobQueued, job.State)
	duplicate, created, err := store.CreateAIJob(model.AIJob{ClientRequestID: "request-1", Kind: model.AIJobTranscription, ProfileID: profile.ID, Input: model.AITranscriptionInput{BVID: "BV1xx411c7mD"}})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, job.ID, duplicate.ID)
	profile.Name, profile.APIKey = "changed", "new-secret"
	_, err = store.PutAIProfile(profile)
	require.NoError(t, err)
	snapshot, _, err := store.AIJobConfig(job.ID)
	require.NoError(t, err)
	assert.Equal(t, "transcriber", snapshot.Name)
	assert.Equal(t, "secret", snapshot.APIKey)

	claimed, err := store.ClaimAIJob(model.AIJobTranscription)
	require.NoError(t, err)
	assert.Equal(t, model.AIJobRunning, claimed.State)
	assert.Equal(t, 1, claimed.Attempts)
	require.NoError(t, store.UpdateAIJobProgress(job.ID, "transcribing", 60))
	result := model.AITranscriptionResult{BVID: "BV1xx411c7mD", Title: "video", Pages: []model.AITranscriptPage{{Page: 1, Segments: []model.AITranscriptSegment{{StartMS: 10, EndMS: 20, Text: "hello"}}}}}
	require.NoError(t, store.FinishAIJob(job.ID, result))

	detail, err := store.AIJob(job.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AIJobSucceeded, detail.State)
	assert.Equal(t, result, detail.Result)
	page, err := store.ListAIJobs(model.AIJobQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Nil(t, page.Items[0].Input)
	assert.Nil(t, page.Items[0].Result)
	assert.ErrorIs(t, store.CancelAIJob(job.ID), ErrAIJobNotCancelable)
	require.NoError(t, store.DeleteAIJob(job.ID))
	require.NoError(t, store.DeleteAIPrompt(prompt.ID))
	require.NoError(t, store.DeleteAIProfile(profile.ID))
}

func TestAIJobInvalidStateTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*Store) error
		want error
	}{
		{name: "missing cancel", run: func(store *Store) error { return store.CancelAIJob("missing") }, want: ErrAIJobNotCancelable},
		{name: "missing retry", run: func(store *Store) error { return store.RetryAIJob("missing") }, want: ErrAIJobNotRetryable},
		{name: "missing delete", run: func(store *Store) error { return store.DeleteAIJob("missing") }, want: ErrAIJobNotTerminal},
		{name: "negative offset", run: func(store *Store) error { _, err := store.ListAIJobs(model.AIJobQuery{Offset: -1}); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.run(openTestStore(t, 112))
			require.Error(t, err)
			if tt.want != nil {
				assert.ErrorIs(t, err, tt.want)
			}
		})
	}
}
