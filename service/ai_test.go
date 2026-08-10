package service

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/internal/aiworkerpb"
	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	job, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "summary", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, Input: model.AISummaryInput{Text: "source"}})
	require.NoError(t, err)
	rpcFailureJob, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "rpc-failure", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, Input: model.AISummaryInput{Text: "rpc-error"}})
	require.NoError(t, err)
	incompleteJob, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "incomplete", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, Input: model.AISummaryInput{Text: "incomplete"}})
	require.NoError(t, err)
	missingSourceJob, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "missing-source", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, Input: model.AISummaryInput{TranscriptionID: "missing"}})
	require.NoError(t, err)
	transcriptionProfile, err := store.PutAIProfile(model.AIProfile{Name: "transcription", Kind: model.AIProfileTranscription, BaseURL: "https://provider.example/v1", Model: "transcription-model", APIKey: "secret", Language: "zh", TimeoutSec: 60, Enabled: true})
	require.NoError(t, err)
	require.NoError(t, store.SaveSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "session"}}))
	transcriptionJob, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "transcription", Kind: model.AIJobTranscription, ProfileID: transcriptionProfile.ID, Input: model.AITranscriptionInput{BVID: "BV1xx411c7mD", Page: 1}})
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
	result, ok := completed.Result.(model.AISummaryResult)
	require.True(t, ok)
	assert.Equal(t, "summary: source", result.Markdown)
	assert.Equal(t, float64(10), result.Usage["input_tokens"])
	assert.Equal(t, 1, completed.Attempts)
	assert.True(t, engine.Status().Connected)
	probe := engine.TestProfile(t.Context(), profile)
	assert.True(t, probe.OK)
	assert.Equal(t, int64(12), probe.LatencyMS)
	assert.Equal(t, 200, probe.ProviderHTTPStatus)
	transcription, err := store.AIJob(transcriptionJob.ID)
	require.NoError(t, err)
	transcriptionResult, ok := transcription.Result.(model.AITranscriptionResult)
	require.True(t, ok)
	assert.Equal(t, "BV1xx411c7mD", transcriptionResult.BVID)
	assert.Equal(t, "spoken text", transcriptionResult.Text())
	assert.Equal(t, float64(12), transcriptionResult.Usage["input_seconds"])
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
