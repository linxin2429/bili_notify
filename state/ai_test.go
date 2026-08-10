package state

import (
	"context"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func configureAutomaticAI(t *testing.T, store *Store, enabled bool) {
	t.Helper()
	_, err := store.PutAIProfile(model.AIProfile{Name: "transcription", Kind: model.AIProfileTranscription, BaseURL: "https://provider.example/v1", Model: "transcribe", APIKey: "secret", TimeoutSec: 60, Enabled: true, Default: true})
	require.NoError(t, err)
	_, err = store.PutAIProfile(model.AIProfile{Name: "summary", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "summary", APIKey: "secret", ContextWindowChars: 10000, TimeoutSec: 60, Enabled: true, Default: true})
	require.NoError(t, err)
	_, err = store.PutAIPrompt(model.AIPromptTemplate{Name: "default", ChunkPrompt: "{{text}}", ReducePrompt: "{{summaries}}", Default: true})
	require.NoError(t, err)
	settings := model.DefaultRuntimeSettings()
	settings.AIAutoProcessingEnabled = enabled
	require.NoError(t, store.PutRuntimeSettings(settings))
}

func TestAutomaticAIPipelineContinuesCollectionTraceAndEnqueuesTerminalNotifications(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 116)
	configureAutomaticAI(t, store, true)
	require.NoError(t, store.PutUP(model.UP{UID: "42", Name: "UP", Enabled: true}))
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	ctx, collection := provider.Tracer("test").Start(t.Context(), "collection.poll_up")
	collectionContext := collection.SpanContext()
	t.Cleanup(func() { collection.End() })

	created, err := store.WithContext(ctx).RecordDynamics("42", []model.Dynamic{{
		ID: "video", BVID: "BV1xx411c7mD", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_AV",
		PublishedAt: time.Now(), Title: "video", TargetURL: "https://www.bilibili.com/video/BV1xx411c7mD",
	}}, []string{"channel"}, DynamicBaselineNone)
	require.NoError(t, err)
	require.Equal(t, 1, created)

	jobs, err := store.AIJobsForDynamic("video", true)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	transcription, summary := jobs[0], jobs[1]
	assert.Equal(t, model.AIJobOriginDynamic, transcription.Origin)
	assert.Equal(t, transcription.ID, summary.DependsOnJobID)
	assert.Equal(t, model.AISummaryInput{TranscriptionID: transcription.ID}, summary.Input)
	for _, job := range jobs {
		parent := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": job.OriginTraceparent})
		assert.Equal(t, collectionContext.TraceID(), trace.SpanContextFromContext(parent).TraceID())
	}
	_, err = store.ClaimAIJob(model.AIJobSummary)
	require.ErrorIs(t, err, ErrNotFound)

	claimed, err := store.ClaimAIJob(model.AIJobTranscription)
	require.NoError(t, err)
	assert.Equal(t, transcription.ID, claimed.ID)
	transcript := model.AITranscriptionResult{BVID: "BV1xx411c7mD", Title: "video", Pages: []model.AITranscriptPage{{Page: 1, Title: "P1", Segments: []model.AITranscriptSegment{{StartMS: 1000, EndMS: 2000, Text: "hello"}}}}}
	require.NoError(t, store.WithContext(ctx).FinishAIJob(transcription.ID, transcript))
	claimed, err = store.ClaimAIJob(model.AIJobSummary)
	require.NoError(t, err)
	assert.Equal(t, summary.ID, claimed.ID)
	require.NoError(t, store.WithContext(ctx).FinishAIJob(summary.ID, model.AISummaryResult{Markdown: "summary"}))

	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 3)
	kinds := []model.DeliveryKind{deliveries[0].EffectiveKind(), deliveries[1].EffectiveKind(), deliveries[2].EffectiveKind()}
	assert.ElementsMatch(t, []model.DeliveryKind{model.DeliveryKindDynamic, model.DeliveryKindAI, model.DeliveryKindAI}, kinds)
	for _, delivery := range deliveries {
		if delivery.EffectiveKind() == model.DeliveryKindAI {
			require.NotNil(t, delivery.AI)
			assert.True(t, delivery.AI.Succeeded)
			assert.NotEmpty(t, delivery.OriginTraceparent)
		}
	}
	require.NoError(t, store.DeleteUP("42"))
	jobs, err = store.AIJobsForDynamic("video", false)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries)
}

func TestAutomaticAIPipelineEligibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		dynamic  model.Dynamic
		baseline DynamicBaselineMode
		enabled  bool
		wantJobs int
	}{
		{name: "direct video", dynamic: model.Dynamic{ID: "direct", BVID: "BV1xx411c7mD", Type: "DYNAMIC_TYPE_AV"}, enabled: true, wantJobs: 2},
		{name: "baseline video", dynamic: model.Dynamic{ID: "baseline", BVID: "BV1xx411c7mD", Type: "DYNAMIC_TYPE_AV"}, baseline: DynamicBaselineAll, enabled: true},
		{name: "forwarded video", dynamic: model.Dynamic{ID: "forward", Type: "DYNAMIC_TYPE_FORWARD", Original: &model.Dynamic{BVID: "BV1xx411c7mD", Type: "DYNAMIC_TYPE_AV"}}, enabled: true},
		{name: "video without bvid", dynamic: model.Dynamic{ID: "missing", Type: "DYNAMIC_TYPE_AV"}, enabled: true},
		{name: "automation disabled", dynamic: model.Dynamic{ID: "disabled", BVID: "BV1xx411c7mD", Type: "DYNAMIC_TYPE_AV"}},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, byte(120+index))
			configureAutomaticAI(t, store, tt.enabled)
			tt.dynamic.UID, tt.dynamic.UPName, tt.dynamic.PublishedAt = "42", "UP", time.Now()
			if tt.baseline == DynamicBaselineAll {
				require.NoError(t, store.PutUP(model.UP{UID: "42"}))
			}
			_, err := store.RecordDynamics("42", []model.Dynamic{tt.dynamic}, nil, tt.baseline)
			require.NoError(t, err)
			jobs, err := store.AIJobsForDynamic(tt.dynamic.ID, false)
			require.NoError(t, err)
			assert.Len(t, jobs, tt.wantJobs)
		})
	}
}

func TestAutomaticAITranscriptionFailureSkipsSummaryAndNotifiesOnce(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 117)
	configureAutomaticAI(t, store, true)
	_, err := store.RecordDynamics("42", []model.Dynamic{{ID: "failed-video", BVID: "BV1xx411c7mD", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_AV", PublishedAt: time.Now()}}, []string{"channel"}, DynamicBaselineNone)
	require.NoError(t, err)
	transcription, err := store.ClaimAIJob(model.AIJobTranscription)
	require.NoError(t, err)
	require.NoError(t, store.FailAIJob(transcription.ID, "provider_error", "failed"))
	jobs, err := store.AIJobsForDynamic("failed-video", false)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	assert.Equal(t, model.AIJobFailed, jobs[0].State)
	assert.Equal(t, model.AIJobSkipped, jobs[1].State)
	_, err = store.ClaimAIJob(model.AIJobSummary)
	require.ErrorIs(t, err, ErrNotFound)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 2)
	var failure *model.AINotification
	for _, delivery := range deliveries {
		if delivery.AI != nil {
			failure = delivery.AI
		}
	}
	require.NotNil(t, failure)
	assert.False(t, failure.Succeeded)
	require.NoError(t, store.RetryAIJob(transcription.ID))
	jobs, err = store.AIJobsForDynamic("failed-video", false)
	require.NoError(t, err)
	assert.Equal(t, model.AIJobQueued, jobs[0].State)
	assert.Equal(t, model.AIJobQueued, jobs[1].State)
}

func TestAutomaticAIConfigurationCannotBeBrokenWhileEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*Store) error
	}{
		{name: "disable default profile", run: func(store *Store) error {
			profiles, err := store.ListAIProfiles()
			if err != nil {
				return err
			}
			for _, profile := range profiles {
				if profile.Kind == model.AIProfileTranscription && profile.Default {
					_, err = store.SetAIProfileEnabled(profile.ID, false)
					return err
				}
			}
			return ErrNotFound
		}},
		{name: "delete default prompt", run: func(store *Store) error {
			prompts, err := store.ListAIPrompts()
			if err != nil {
				return err
			}
			for _, prompt := range prompts {
				if prompt.Default {
					return store.DeleteAIPrompt(prompt.ID)
				}
			}
			return ErrNotFound
		}},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, byte(130+index))
			configureAutomaticAI(t, store, true)
			err := tt.run(store)
			require.Error(t, err)
			require.NoError(t, store.ValidateAutoAIConfiguration(), "failed mutation must roll back")
		})
	}
}

func TestPutRuntimeSettingsRejectsAutomaticAIWithoutDefaults(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 119)
	settings := model.DefaultRuntimeSettings()
	settings.AIAutoProcessingEnabled = true
	require.Error(t, store.PutRuntimeSettings(settings))
	_, err := store.RuntimeSettings()
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAIProfileAvailabilityMaintainsDefaultInvariant(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 115)
	profile, err := store.PutAIProfile(model.AIProfile{Name: "text", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", ContextWindowChars: 10000, TimeoutSec: 60, Enabled: true, Default: true})
	require.NoError(t, err)

	disabled, err := store.SetAIProfileEnabled(profile.ID, false)
	require.NoError(t, err)
	assert.False(t, disabled.Enabled)
	assert.False(t, disabled.Default)

	enabled, err := store.SetAIProfileEnabled(profile.ID, true)
	require.NoError(t, err)
	assert.True(t, enabled.Enabled)
	assert.False(t, enabled.Default)

	enabled.Enabled, enabled.Default = false, true
	normalized, err := store.PutAIProfile(enabled)
	require.NoError(t, err)
	assert.False(t, normalized.Enabled)
	assert.False(t, normalized.Default)

	_, err = store.SetAIProfileEnabled("missing", true)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAIJobLifecycleAndEncryptedDetail(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, 111)
	profile, err := store.PutAIProfile(model.AIProfile{Name: "transcriber", Kind: model.AIProfileTranscription, BaseURL: "https://openrouter.ai/api/v1", Model: "openai/gpt-transcribe", APIKey: "secret", Language: "zh", TimeoutSec: 600, Enabled: true, Default: true})
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
	profile, err := store.PutAIProfile(model.AIProfile{Name: "text", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", Temperature: 0.2, MaxOutputTokens: 1024, ContextWindowChars: 10000, TimeoutSec: 60, Enabled: true})
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
	profile, err := store.PutAIProfile(model.AIProfile{Name: "text", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", Temperature: 0.2, MaxOutputTokens: 1024, ContextWindowChars: 10000, TimeoutSec: 60, Enabled: true})
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
