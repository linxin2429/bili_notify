package service

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/internal/aiworkerpb"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type fakeAIWorker struct {
	aiworkerpb.UnimplementedAIWorkerServer
}

func TestAIProfileTestMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		code     string
		fallback string
		want     string
	}{
		{name: "known provider error", code: "provider_authentication", fallback: "english", want: "模型供应商拒绝了 API Key"},
		{name: "unknown worker error", code: "unknown", fallback: "detail", want: "detail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, aiProfileTestMessage(tt.code, tt.fallback))
		})
	}
}

func (fakeAIWorker) GetCapabilities(context.Context, *aiworkerpb.CapabilitiesRequest) (*aiworkerpb.CapabilitiesResponse, error) {
	return &aiworkerpb.CapabilitiesResponse{Version: "test", YtDlpAvailable: true, FfmpegAvailable: true}, nil
}

func (fakeAIWorker) TestProvider(context.Context, *aiworkerpb.TestProviderRequest) (*aiworkerpb.TestProviderResponse, error) {
	return &aiworkerpb.TestProviderResponse{Ok: true, LatencyMs: 12, Message: "模型响应正常", ProviderHttpStatus: 200}, nil
}

func (fakeAIWorker) Summarize(request *aiworkerpb.SummaryRequest, stream grpc.ServerStreamingServer[aiworkerpb.WorkerEvent]) error {
	switch request.Text {
	case "rpc-error":
		return status.Error(codes.ResourceExhausted, `{"code":"rate_limited","message":"retry later"}`)
	case "incomplete":
		return nil
	}
	if err := stream.Send(&aiworkerpb.WorkerEvent{Event: &aiworkerpb.WorkerEvent_Progress{Progress: &aiworkerpb.Progress{Stage: "summarizing_chunks", Percent: 50}}}); err != nil {
		return err
	}
	usage, err := structpb.NewStruct(map[string]any{"input_tokens": 10})
	if err != nil {
		return err
	}
	return stream.Send(&aiworkerpb.WorkerEvent{Event: &aiworkerpb.WorkerEvent_Summary{Summary: &aiworkerpb.SummaryResult{Markdown: "summary: " + request.Text, Usage: usage}}})
}

func (fakeAIWorker) Transcribe(request *aiworkerpb.TranscribeRequest, stream grpc.ServerStreamingServer[aiworkerpb.WorkerEvent]) error {
	if err := stream.Send(&aiworkerpb.WorkerEvent{Event: &aiworkerpb.WorkerEvent_Progress{Progress: &aiworkerpb.Progress{Stage: "transcribing", Percent: 40}}}); err != nil {
		return err
	}
	usage, err := structpb.NewStruct(map[string]any{"input_seconds": 12})
	if err != nil {
		return err
	}
	return stream.Send(&aiworkerpb.WorkerEvent{Event: &aiworkerpb.WorkerEvent_Transcription{Transcription: &aiworkerpb.TranscriptionResult{
		Bvid:  request.Bvid,
		Title: "video",
		Pages: []*aiworkerpb.TranscriptPage{{
			Page: 1, Cid: "42", Title: "page", DurationMs: 12000,
			Segments: []*aiworkerpb.TranscriptSegment{{StartMs: 100, EndMs: 900, Text: "spoken text"}},
		}},
		Usage: usage,
	}}})
}

func TestAIEngineExecutesQueuedSummaryOverUnixRPC(t *testing.T) {
	t.Parallel()
	store := openServiceTestStore(t)
	profile, err := store.PutAIProfile(model.AIProfile{Name: "text", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", Temperature: 0.2, MaxOutputTokens: 1024, ContextWindowChars: 10000, TimeoutSec: 60, Enabled: true})
	require.NoError(t, err)
	prompt, err := store.PutAIPrompt(model.AIPromptTemplate{Name: "prompt", ChunkPrompt: "{{text}}", ReducePrompt: "{{summaries}}"})
	require.NoError(t, err)
	job, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "summary", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, SummaryInput: &model.AISummaryInput{Text: "source"}})
	require.NoError(t, err)
	rpcFailureJob, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "rpc-failure", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, SummaryInput: &model.AISummaryInput{Text: "rpc-error"}})
	require.NoError(t, err)
	incompleteJob, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "incomplete", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, SummaryInput: &model.AISummaryInput{Text: "incomplete"}})
	require.NoError(t, err)
	missingSourceJob, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "missing-source", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, SummaryInput: &model.AISummaryInput{TranscriptionID: "missing"}})
	require.NoError(t, err)
	transcriptionProfile, err := store.PutAIProfile(model.AIProfile{Name: "transcription", Kind: model.AIProfileTranscription, BaseURL: "https://provider.example/v1", Model: "transcription-model", APIKey: "secret", Language: "zh", TimeoutSec: 60, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, store.SaveSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "session"}}))
	putServiceTestChannel(t, store)
	transcriptionJob, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "transcription", Kind: model.AIJobTranscription, ProfileID: transcriptionProfile.ID, TranscriptionInput: &model.AITranscriptionInput{BVID: "BV1xx411c7mD", Page: 1}})
	require.NoError(t, err)

	socket := filepath.Join(t.TempDir(), "worker.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	rpcServer := grpc.NewServer()
	aiworkerpb.RegisterAIWorkerServer(rpcServer, fakeAIWorker{})
	t.Cleanup(rpcServer.Stop)
	serveDone := make(chan error, 1)
	go func() { serveDone <- rpcServer.Serve(listener) }()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	engine := NewAIEngine(store, socket, testLogger(), NewEventBus())
	runDone := make(chan error, 1)
	go func() { runDone <- engine.Run(ctx) }()
	require.Eventually(t, func() bool {
		currentSummary, summaryErr := store.AIJob(job.ID)
		currentTranscription, transcriptionErr := store.AIJob(transcriptionJob.ID)
		rpcFailure, rpcErr := store.AIJob(rpcFailureJob.ID)
		incomplete, incompleteErr := store.AIJob(incompleteJob.ID)
		missingSource, sourceErr := store.AIJob(missingSourceJob.ID)
		return summaryErr == nil && transcriptionErr == nil && rpcErr == nil && incompleteErr == nil && sourceErr == nil &&
			currentSummary.State == model.AIJobSucceeded && currentTranscription.State == model.AIJobSucceeded &&
			rpcFailure.State == model.AIJobFailed && incomplete.State == model.AIJobFailed && missingSource.State == model.AIJobFailed
	}, 5*time.Second, 20*time.Millisecond)

	completed, err := store.AIJob(job.ID)
	require.NoError(t, err)
	require.NotNil(t, completed.SummaryResult)
	result := *completed.SummaryResult
	assert.Equal(t, "summary: source", result.Markdown)
	assert.JSONEq(t, `{"input_tokens":10}`, string(result.Usage))
	assert.Equal(t, 1, completed.Attempts)
	assert.True(t, engine.Status().Connected)
	probe := engine.TestProfile(t.Context(), profile)
	assert.True(t, probe.OK)
	assert.Equal(t, int64(12), probe.LatencyMS)
	assert.Equal(t, 200, probe.ProviderHTTPStatus)
	transcription, err := store.AIJob(transcriptionJob.ID)
	require.NoError(t, err)
	require.NotNil(t, transcription.TranscriptionResult)
	transcriptionResult := *transcription.TranscriptionResult
	assert.Equal(t, "BV1xx411c7mD", transcriptionResult.BVID)
	assert.Equal(t, "spoken text", transcriptionResult.Text())
	assert.JSONEq(t, `{"input_seconds":12}`, string(transcriptionResult.Usage))
	rpcFailure, err := store.AIJob(rpcFailureJob.ID)
	require.NoError(t, err)
	assert.Equal(t, "rate_limited", rpcFailure.ErrorCode)
	assert.Equal(t, "retry later", rpcFailure.LastError)
	incomplete, err := store.AIJob(incompleteJob.ID)
	require.NoError(t, err)
	assert.Equal(t, "worker_incomplete", incomplete.ErrorCode)
	missingSource, err := store.AIJob(missingSourceJob.ID)
	require.NoError(t, err)
	assert.Equal(t, "source_unavailable", missingSource.ErrorCode)

	cancel()
	require.NoError(t, <-runDone)
	rpcServer.Stop()
	require.NoError(t, <-serveDone)
}

func TestAIEnginePropagatesWorkbenchTraceAcrossUnixRPC(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	store := openServiceTestStore(t)
	profile, err := store.PutAIProfile(model.AIProfile{Name: "text", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", ContextWindowChars: 10000, TimeoutSec: 60, Enabled: true})
	require.NoError(t, err)
	prompt, err := store.PutAIPrompt(model.AIPromptTemplate{Name: "prompt", ChunkPrompt: "{{text}}", ReducePrompt: "{{summaries}}"})
	require.NoError(t, err)
	originCtx, origin := provider.Tracer("test").Start(t.Context(), "POST /api/v4/ai/summaries", trace.WithSpanKind(trace.SpanKindServer))
	originContext := origin.SpanContext()
	job, _, err := store.WithContext(originCtx).CreateAIJob(model.AIJob{ClientRequestID: "traced-summary", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, SummaryInput: &model.AISummaryInput{Text: "source"}})
	require.NoError(t, err)
	origin.End()

	socket := filepath.Join(t.TempDir(), "traced-worker.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	rpcServer := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler(otelgrpc.WithTracerProvider(provider), otelgrpc.WithPropagators(propagator))))
	aiworkerpb.RegisterAIWorkerServer(rpcServer, fakeAIWorker{})
	t.Cleanup(rpcServer.Stop)
	serveDone := make(chan error, 1)
	go func() { serveDone <- rpcServer.Serve(listener) }()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	engine := NewAIEngine(store, socket, testLogger(), NewEventBus(), WithAITelemetry(provider, metricnoop.NewMeterProvider(), propagator))
	runDone := make(chan error, 1)
	go func() { runDone <- engine.Run(ctx) }()
	require.Eventually(t, func() bool {
		current, jobErr := store.AIJob(job.ID)
		return jobErr == nil && current.State == model.AIJobSucceeded
	}, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		var execute, client, server bool
		for _, span := range recorder.Ended() {
			if span.SpanContext().TraceID() != originContext.TraceID() {
				continue
			}
			execute = execute || span.Name() == "ai.job.execute"
			client = client || span.SpanKind() == trace.SpanKindClient && strings.Contains(span.Name(), "Summarize")
			server = server || span.SpanKind() == trace.SpanKindServer && strings.Contains(span.Name(), "Summarize")
		}
		return execute && client && server
	}, 5*time.Second, 20*time.Millisecond)

	cancel()
	require.NoError(t, <-runDone)
	rpcServer.Stop()
	require.NoError(t, <-serveDone)
}

func TestAIEngineContinuesDynamicCollectionTraceThroughAutomaticPipeline(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	store := openServiceTestStore(t)
	transcriptionProfile, err := store.PutAIProfile(model.AIProfile{
		Name: "transcription", Kind: model.AIProfileTranscription, BaseURL: "https://provider.example/v1",
		Model: "transcription-model", APIKey: "secret", Language: "zh", TimeoutSec: 60, Enabled: true, Default: true,
	})
	require.NoError(t, err)
	_, err = store.PutAIProfile(model.AIProfile{
		Name: "summary", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "summary-model",
		APIKey: "secret", ContextWindowChars: 10000, TimeoutSec: 60, Enabled: true, Default: true,
	})
	require.NoError(t, err)
	_, err = store.PutAIPrompt(model.AIPromptTemplate{Name: "prompt", ChunkPrompt: "{{text}}", ReducePrompt: "{{summaries}}", Default: true})
	require.NoError(t, err)
	settings := model.DefaultRuntimeSettings()
	settings.AIAutoProcessingEnabled = true
	require.NoError(t, store.PutRuntimeSettings(settings))
	require.NoError(t, store.SaveSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "session"}}))
	putServiceTestChannel(t, store)

	originCtx, origin := provider.Tracer("test").Start(t.Context(), "collection.poll_up")
	originContext := origin.SpanContext()
	created, err := recordDynamicsForTest(store.WithContext(originCtx), "42", []model.Dynamic{{
		ID: "dynamic-video", BVID: "BV1xx411c7mD", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_AV",
		Title: "video", TargetURL: "https://www.bilibili.com/video/BV1xx411c7mD", PublishedAt: time.Now(),
	}}, []string{"channel"}, state.DynamicBaselineNone)
	require.NoError(t, err)
	assert.Equal(t, 1, created)
	origin.End()
	jobs, err := store.AIJobsForContent(model.ContentID(model.PlatformBilibili, "dynamic-video"), false)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	assert.Equal(t, transcriptionProfile.ID, jobs[0].ProfileID)

	socketDir, err := os.MkdirTemp("", "bn-ai-trace-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(socketDir)) })
	socket := filepath.Join(socketDir, "worker.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	rpcServer := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler(otelgrpc.WithTracerProvider(provider), otelgrpc.WithPropagators(propagator))))
	aiworkerpb.RegisterAIWorkerServer(rpcServer, fakeAIWorker{})
	t.Cleanup(rpcServer.Stop)
	serveDone := make(chan error, 1)
	go func() { serveDone <- rpcServer.Serve(listener) }()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	engine := NewAIEngine(store, socket, testLogger(), NewEventBus(), WithAITelemetry(provider, metricnoop.NewMeterProvider(), propagator))
	runDone := make(chan error, 1)
	go func() { runDone <- engine.Run(ctx) }()
	require.Eventually(t, func() bool {
		current, jobErr := store.AIJobsForContent(model.ContentID(model.PlatformBilibili, "dynamic-video"), false)
		return jobErr == nil && len(current) == 2 && current[0].State == model.AIJobSucceeded && current[1].State == model.AIJobSucceeded
	}, 5*time.Second, 20*time.Millisecond)

	require.Eventually(t, func() bool {
		var transcriptionExecute, summaryExecute sdktrace.ReadOnlySpan
		var transcriptionClient, transcriptionServer, summaryClient, summaryServer bool
		for _, span := range recorder.Ended() {
			if span.SpanContext().TraceID() != originContext.TraceID() {
				continue
			}
			if span.Name() == "ai.job.execute" {
				for _, attribute := range span.Attributes() {
					if string(attribute.Key) != "ai.job.kind" {
						continue
					}
					if attribute.Value.AsString() == string(model.AIJobTranscription) {
						transcriptionExecute = span
					} else if attribute.Value.AsString() == string(model.AIJobSummary) {
						summaryExecute = span
					}
				}
			}
			transcriptionClient = transcriptionClient || span.SpanKind() == trace.SpanKindClient && strings.Contains(span.Name(), "Transcribe")
			transcriptionServer = transcriptionServer || span.SpanKind() == trace.SpanKindServer && strings.Contains(span.Name(), "Transcribe")
			summaryClient = summaryClient || span.SpanKind() == trace.SpanKindClient && strings.Contains(span.Name(), "Summarize")
			summaryServer = summaryServer || span.SpanKind() == trace.SpanKindServer && strings.Contains(span.Name(), "Summarize")
		}
		return transcriptionExecute != nil && summaryExecute != nil &&
			summaryExecute.Parent().SpanID() == transcriptionExecute.SpanContext().SpanID() &&
			transcriptionClient && transcriptionServer && summaryClient && summaryServer
	}, 5*time.Second, 20*time.Millisecond)

	deliveries, err := store.QueryDeliveries(state.DeliveryQuery{Limit: 10})
	require.NoError(t, err)
	aiDeliveries := 0
	for _, delivery := range deliveries {
		if delivery.Kind != model.DeliveryKindAI {
			continue
		}
		aiDeliveries++
		parent := propagator.Extract(context.Background(), propagation.MapCarrier{"traceparent": delivery.OriginTraceparent})
		assert.Equal(t, originContext.TraceID(), trace.SpanContextFromContext(parent).TraceID())
	}
	assert.Equal(t, 2, aiDeliveries)

	cancel()
	require.NoError(t, <-runDone)
	rpcServer.Stop()
	require.NoError(t, <-serveDone)
}
