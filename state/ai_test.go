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
		{name: "missing profile", run: func(store *Store) error { _, err := store.AIProfile("missing"); return err }, want: ErrNotFound},
		{name: "missing prompt", run: func(store *Store) error { _, err := store.AIPrompt("missing"); return err }, want: ErrNotFound},
		{name: "missing job", run: func(store *Store) error { _, err := store.AIJob("missing"); return err }, want: ErrNotFound},
		{name: "missing job config", run: func(store *Store) error { _, _, err := store.AIJobConfig("missing"); return err }, want: ErrNotFound},
		{name: "delete missing profile", run: func(store *Store) error { return store.DeleteAIProfile("missing") }, want: ErrNotFound},
		{name: "delete missing prompt", run: func(store *Store) error { return store.DeleteAIPrompt("missing") }, want: ErrNotFound},
		{name: "negative offset", run: func(store *Store) error { _, err := store.ListAIJobs(model.AIJobQuery{Offset: -1}); return err }},
		{name: "negative progress", run: func(store *Store) error { return store.UpdateAIJobProgress("missing", "stage", -1) }},
		{name: "overflow progress", run: func(store *Store) error { return store.UpdateAIJobProgress("missing", "stage", 101) }},
		{name: "empty progress stage", run: func(store *Store) error { return store.UpdateAIJobProgress("missing", "", 10) }},
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

func TestAIJobCreationValidation(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 114)
	profile, err := store.PutAIProfile(model.AIProfile{Name: "text", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", Temperature: 0.2, MaxOutputTokens: 1024, ContextWindowChars: 10000, TimeoutSec: 60})
	require.NoError(t, err)
	tests := []struct {
		name string
		job  model.AIJob
	}{
		{name: "invalid kind", job: model.AIJob{ClientRequestID: "invalid-kind", Kind: "invalid", ProfileID: profile.ID}},
		{name: "empty request id", job: model.AIJob{Kind: model.AIJobTranscription, ProfileID: profile.ID}},
		{name: "missing profile", job: model.AIJob{ClientRequestID: "missing-profile", Kind: model.AIJobTranscription, ProfileID: "missing"}},
		{name: "summary missing prompt", job: model.AIJob{ClientRequestID: "missing-prompt-id", Kind: model.AIJobSummary, ProfileID: profile.ID}},
		{name: "missing prompt", job: model.AIJob{ClientRequestID: "missing-prompt", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: "missing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, created, err := store.CreateAIJob(tt.job)
			require.Error(t, err)
			assert.False(t, created)
		})
	}
}

func TestAIJobFailureRetryLifecycle(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 113)
	profile, err := store.PutAIProfile(model.AIProfile{Name: "text", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", Temperature: 0.2, MaxOutputTokens: 1024, ContextWindowChars: 10000, TimeoutSec: 60})
	require.NoError(t, err)
	prompt, err := store.PutAIPrompt(model.AIPromptTemplate{Name: "prompt", ChunkPrompt: "{{text}}", ReducePrompt: "{{summaries}}"})
	require.NoError(t, err)
	job, created, err := store.CreateAIJob(model.AIJob{ClientRequestID: "failure-retry", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, Input: model.AISummaryInput{Text: "source"}})
	require.NoError(t, err)
	assert.True(t, created)

	_, err = store.ClaimAIJob(model.AIJobSummary)
	require.NoError(t, err)
	require.NoError(t, store.FailAIJob(job.ID, "provider_error", "provider rejected request"))
	failed, err := store.AIJob(job.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AIJobFailed, failed.State)
	assert.Equal(t, "provider_error", failed.ErrorCode)
	assert.Equal(t, "provider rejected request", failed.LastError)
	assert.NotZero(t, failed.FinishedAt)

	require.NoError(t, store.RetryAIJob(job.ID))
	retried, err := store.AIJob(job.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AIJobQueued, retried.State)
	assert.Empty(t, retried.ErrorCode)
	assert.Empty(t, retried.LastError)
	assert.Zero(t, retried.FinishedAt)

	_, err = store.ClaimAIJob(model.AIJobSummary)
	require.NoError(t, err)
	require.NoError(t, store.CancelAIJob(job.ID))
	require.NoError(t, store.DeleteAIJob(job.ID))
	require.NoError(t, store.DeleteAIPrompt(prompt.ID))
	require.NoError(t, store.DeleteAIProfile(profile.ID))
}
