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
	"google.golang.org/protobuf/types/known/structpb"
)

type fakeAIWorker struct {
	aiworkerpb.UnimplementedAIWorkerServer
}

func (fakeAIWorker) GetCapabilities(context.Context, *aiworkerpb.CapabilitiesRequest) (*aiworkerpb.CapabilitiesResponse, error) {
	return &aiworkerpb.CapabilitiesResponse{Version: "test", YtDlpAvailable: true, FfmpegAvailable: true}, nil
}

func (fakeAIWorker) Summarize(request *aiworkerpb.SummaryRequest, stream grpc.ServerStreamingServer[aiworkerpb.WorkerEvent]) error {
	if err := stream.Send(&aiworkerpb.WorkerEvent{Event: &aiworkerpb.WorkerEvent_Progress{Progress: &aiworkerpb.Progress{Stage: "summarizing_chunks", Percent: 50}}}); err != nil {
		return err
	}
	usage, err := structpb.NewStruct(map[string]any{"input_tokens": 10})
	if err != nil {
		return err
	}
	return stream.Send(&aiworkerpb.WorkerEvent{Event: &aiworkerpb.WorkerEvent_Summary{Summary: &aiworkerpb.SummaryResult{Markdown: "summary: " + request.Text, Usage: usage}}})
}

func TestAIEngineExecutesQueuedSummaryOverUnixRPC(t *testing.T) {
	t.Parallel()
	store := openServiceTestStore(t)
	profile, err := store.PutAIProfile(model.AIProfile{Name: "text", Kind: model.AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", Temperature: 0.2, MaxOutputTokens: 1024, ContextWindowChars: 10000, TimeoutSec: 60})
	require.NoError(t, err)
	prompt, err := store.PutAIPrompt(model.AIPromptTemplate{Name: "prompt", ChunkPrompt: "{{text}}", ReducePrompt: "{{summaries}}"})
	require.NoError(t, err)
	job, _, err := store.CreateAIJob(model.AIJob{ClientRequestID: "summary", Kind: model.AIJobSummary, ProfileID: profile.ID, PromptID: prompt.ID, Input: model.AISummaryInput{Text: "source"}})
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
		current, getErr := store.AIJob(job.ID)
		return getErr == nil && current.State == model.AIJobSucceeded
	}, 5*time.Second, 20*time.Millisecond)

	completed, err := store.AIJob(job.ID)
	require.NoError(t, err)
	result, ok := completed.Result.(model.AISummaryResult)
	require.True(t, ok)
	assert.Equal(t, "summary: source", result.Markdown)
	assert.Equal(t, float64(10), result.Usage["input_tokens"])
	assert.Equal(t, 1, completed.Attempts)
	assert.True(t, engine.Status().Connected)

	cancel()
	require.NoError(t, <-runDone)
	rpcServer.Stop()
	require.NoError(t, <-serveDone)
}
