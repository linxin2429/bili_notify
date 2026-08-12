package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/linxin2429/bili_notify/internal/aiworkerpb"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

const (
	defaultAIFailureCacheTTL = 24 * time.Hour
	defaultAICacheMaxBytes   = int64(5 << 30)
)

type AIWorkerStatus struct {
	Connected            bool      `json:"connected"`
	Version              string    `json:"version,omitempty"`
	YTDLPAvailable       bool      `json:"yt_dlp_available"`
	FFmpegAvailable      bool      `json:"ffmpeg_available"`
	ActiveTranscriptions int       `json:"active_transcriptions"`
	ActiveSummaries      int       `json:"active_summaries"`
	CacheBytes           int64     `json:"cache_bytes"`
	LastCheckedAt        time.Time `json:"last_checked_at,omitzero"`
	LastError            string    `json:"last_error,omitempty"`
}

type AIProfileTestResult struct {
	OK                 bool   `json:"ok"`
	LatencyMS          int64  `json:"latency_ms"`
	Message            string `json:"message"`
	ErrorCode          string `json:"error_code,omitempty"`
	ProviderHTTPStatus int    `json:"provider_http_status,omitempty"`
	ProviderError      string `json:"provider_error,omitempty"`
}

type AIEngine struct {
	store          *state.Store
	socketPath     string
	logger         *slog.Logger
	events         *EventBus
	wake           chan struct{}
	statusMu       sync.RWMutex
	status         AIWorkerStatus
	clientMu       sync.RWMutex
	client         aiworkerpb.AIWorkerClient
	runningMu      sync.Mutex
	running        map[string]context.CancelFunc
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	tracer         trace.Tracer
	propagator     propagation.TextMapPropagator
}

type AIEngineOption func(*AIEngine)

// WithAITelemetry connects AI scheduling and gRPC calls to the process providers.
func WithAITelemetry(tracerProvider trace.TracerProvider, meterProvider metric.MeterProvider, propagator propagation.TextMapPropagator) AIEngineOption {
	return func(engine *AIEngine) {
		engine.tracerProvider, engine.meterProvider, engine.propagator = tracerProvider, meterProvider, propagator
		engine.tracer = tracerProvider.Tracer("github.com/linxin2429/bili_notify/service/ai")
	}
}

func NewAIEngine(store *state.Store, socketPath string, logger *slog.Logger, events *EventBus, options ...AIEngineOption) *AIEngine {
	tp, mp := tracenoop.NewTracerProvider(), metricnoop.NewMeterProvider()
	engine := &AIEngine{store: store, socketPath: socketPath, logger: logger, events: events, wake: make(chan struct{}, 1), running: make(map[string]context.CancelFunc),
		tracerProvider: tp, meterProvider: mp, tracer: tp.Tracer("github.com/linxin2429/bili_notify/service/ai"), propagator: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})}
	for _, option := range options {
		option(engine)
	}
	return engine
}

func (e *AIEngine) Status() AIWorkerStatus {
	e.statusMu.RLock()
	defer e.statusMu.RUnlock()
	return e.status
}

func (e *AIEngine) Notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
	e.events.Publish(TopicAIJobs)
}

func (e *AIEngine) TestProfile(ctx context.Context, profile model.AIProfile) AIProfileTestResult {
	e.clientMu.RLock()
	client := e.client
	e.clientMu.RUnlock()
	if client == nil {
		return AIProfileTestResult{Message: "AI Worker 不可用", ErrorCode: "worker_unavailable"}
	}
	timeout := min(time.Duration(profile.TimeoutSec)*time.Second, 20*time.Second)
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	result, err := client.TestProvider(testCtx, &aiworkerpb.TestProviderRequest{Kind: string(profile.Kind), Provider: profileProto(profile)})
	if err != nil {
		code, message := "worker_unavailable", "AI Worker 不可用"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(testCtx.Err(), context.DeadlineExceeded) {
			code, message = "provider_timeout", "模型连通性检测超时"
		}
		return AIProfileTestResult{LatencyMS: time.Since(started).Milliseconds(), Message: message, ErrorCode: code}
	}
	message := result.Message
	if !result.Ok {
		message = aiProfileTestMessage(result.ErrorCode, result.Message)
	}
	return AIProfileTestResult{
		OK: result.Ok, LatencyMS: result.LatencyMs, Message: message, ErrorCode: result.ErrorCode,
		ProviderHTTPStatus: int(result.ProviderHttpStatus), ProviderError: result.ProviderError,
	}
}

func aiProfileTestMessage(code, fallback string) string {
	messages := map[string]string{
		"provider_unreachable":      "无法连接模型供应商",
		"provider_timeout":          "模型供应商响应超时",
		"provider_authentication":   "模型供应商拒绝了 API Key",
		"provider_rate_limited":     "模型供应商限制了请求频率",
		"provider_model_not_found":  "模型或供应商接口不存在",
		"provider_failure":          "模型供应商返回错误",
		"provider_invalid_response": "模型供应商返回了无效响应",
		"invalid_profile_kind":      "模型配置用途无效",
		"worker_failure":            "AI Worker 检测模型时发生内部错误",
	}
	if message := messages[code]; message != "" {
		return message
	}
	return fallback
}

func (e *AIEngine) CancelJob(id string) error {
	if err := e.store.CancelAIJob(id); err != nil {
		return err
	}
	e.runningMu.Lock()
	cancel := e.running[id]
	e.runningMu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.events.Publish(TopicAIJobs)
	return nil
}

func (e *AIEngine) Run(ctx context.Context) error {
	if _, err := e.store.InterruptRunningAIJobs(); err != nil {
		return fmt.Errorf("interrupting stale AI jobs: %w", err)
	}
	connection, err := grpc.NewClient(
		"passthrough:///"+e.socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", e.socketPath)
		}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20), grpc.MaxCallSendMsgSize(16<<20)),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler(
			otelgrpc.WithTracerProvider(e.tracerProvider), otelgrpc.WithMeterProvider(e.meterProvider), otelgrpc.WithPropagators(e.propagator),
			otelgrpc.WithFilter(func(info *stats.RPCTagInfo) bool { return !strings.HasSuffix(info.FullMethodName, "/GetCapabilities") }),
		)),
	)
	if err != nil {
		return fmt.Errorf("creating AI worker client: %w", err)
	}
	defer connection.Close()
	client := aiworkerpb.NewAIWorkerClient(connection)
	e.clientMu.Lock()
	e.client = client
	e.clientMu.Unlock()
	defer func() {
		e.clientMu.Lock()
		e.client = nil
		e.clientMu.Unlock()
	}()
	var workers sync.WaitGroup
	workers.Add(3)
	go func() { defer workers.Done(); e.dispatch(ctx, client, model.AIJobTranscription) }()
	for range 2 {
		go func() { defer workers.Done(); e.dispatch(ctx, client, model.AIJobSummary) }()
	}
	e.monitor(ctx, client)
	workers.Wait()
	return nil
}

func (e *AIEngine) monitor(ctx context.Context, client aiworkerpb.AIWorkerClient) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	e.refreshStatus(ctx, client)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.refreshStatus(ctx, client)
		}
	}
}

func (e *AIEngine) refreshStatus(parent context.Context, client aiworkerpb.AIWorkerClient) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	capabilities, err := client.GetCapabilities(ctx, &aiworkerpb.CapabilitiesRequest{})
	next := AIWorkerStatus{Connected: err == nil, LastCheckedAt: time.Now()}
	if err != nil {
		next.LastError = "AI Worker 不可用"
	} else {
		next.Version = capabilities.Version
		next.YTDLPAvailable = capabilities.YtDlpAvailable
		next.FFmpegAvailable = capabilities.FfmpegAvailable
		next.ActiveTranscriptions = int(capabilities.ActiveTranscriptions)
		next.ActiveSummaries = int(capabilities.ActiveSummaries)
		next.CacheBytes = capabilities.CacheBytes
	}
	e.statusMu.Lock()
	changed := e.status != next
	e.status = next
	e.statusMu.Unlock()
	if changed {
		e.events.Publish(TopicAIStatus)
		if next.Connected {
			select {
			case e.wake <- struct{}{}:
			default:
			}
		}
	}
}

func (e *AIEngine) dispatch(ctx context.Context, client aiworkerpb.AIWorkerClient, kind model.AIJobKind) {
	for {
		if !e.Status().Connected {
			select {
			case <-ctx.Done():
				return
			case <-e.wake:
			case <-time.After(time.Second):
			}
			continue
		}
		job, err := e.store.ClaimAIJob(kind)
		if err == nil {
			e.events.Publish(TopicAIJobs)
			e.execute(ctx, client, job)
			continue
		}
		if !errors.Is(err, state.ErrNotFound) {
			e.logger.ErrorContext(ctx, "claiming AI job failed", "event", "ai.job.claim_failed", "kind", kind, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		case <-time.After(time.Second):
		}
	}
}

func (e *AIEngine) execute(parent context.Context, client aiworkerpb.AIWorkerClient, job model.AIJob) {
	carrier := propagation.MapCarrier{"traceparent": job.OriginTraceparent, "tracestate": job.OriginTracestate}
	parent = e.propagator.Extract(parent, carrier)
	ctx, span := e.tracer.Start(parent, "ai.job.execute", trace.WithAttributes(
		attribute.String("ai.job.id", job.ID), attribute.String("ai.job.kind", string(job.Kind)),
		attribute.String("ai.job.origin", string(job.Origin)), attribute.String("content.dynamic.id", job.SourceContentID),
	))
	ctx, cancel := context.WithCancel(ctx)
	e.runningMu.Lock()
	e.running[job.ID] = cancel
	e.runningMu.Unlock()
	defer func() {
		span.End()
		cancel()
		e.runningMu.Lock()
		delete(e.running, job.ID)
		e.runningMu.Unlock()
		e.events.Publish(TopicAIJobs | TopicAIStatus)
	}()
	dependentCarrier := propagation.MapCarrier{}
	e.propagator.Inject(ctx, dependentCarrier)
	if err := e.store.WithContext(ctx).UpdateDependentAITrace(job.ID, dependentCarrier.Get("traceparent"), dependentCarrier.Get("tracestate")); err != nil {
		e.logger.ErrorContext(ctx, "updating dependent AI trace failed", "event", "ai.job.trace_update_failed", "job_id", job.ID, "error", err)
	}
	profile, prompt, err := e.store.AIJobConfig(job.ID)
	if err != nil {
		e.fail(ctx, job.ID, "config_unavailable", "AI 任务配置快照不可用")
		return
	}
	provider := profileProto(profile)
	var stream grpc.ServerStreamingClient[aiworkerpb.WorkerEvent]
	switch job.Kind {
	case model.AIJobTranscription:
		if job.TranscriptionInput == nil {
			e.fail(ctx, job.ID, "invalid_input", "转写任务输入无效")
			return
		}
		input := *job.TranscriptionInput
		session, sessionErr := e.store.Session()
		if sessionErr != nil {
			e.fail(ctx, job.ID, "bilibili_authentication", "B站登录不可用")
			return
		}
		stream, err = client.Transcribe(ctx, &aiworkerpb.TranscribeRequest{
			JobId: job.ID, Bvid: input.BVID, Page: int32(input.Page), Cookies: session.Cookies, Provider: provider,
			FailureCacheTtlSec: int64(defaultAIFailureCacheTTL / time.Second), CacheMaxBytes: defaultAICacheMaxBytes,
		})
	case model.AIJobSummary:
		if job.SummaryInput == nil {
			e.fail(ctx, job.ID, "invalid_input", "总结任务输入无效")
			return
		}
		input := *job.SummaryInput
		text := input.Text
		if input.TranscriptionID != "" {
			source, sourceErr := e.store.AIJob(input.TranscriptionID)
			if sourceErr != nil || source.State != model.AIJobSucceeded {
				e.fail(ctx, job.ID, "source_unavailable", "来源转写任务不可用")
				return
			}
			if source.TranscriptionResult == nil {
				e.fail(ctx, job.ID, "source_unavailable", "来源转写结果无效")
				return
			}
			text = source.TranscriptionResult.Text()
		}
		if prompt == nil {
			e.fail(ctx, job.ID, "prompt_unavailable", "提示词模板不可用")
			return
		}
		stream, err = client.Summarize(ctx, &aiworkerpb.SummaryRequest{
			JobId: job.ID, Text: text, Provider: provider, SystemPrompt: prompt.SystemPrompt, ChunkPrompt: prompt.ChunkPrompt, ReducePrompt: prompt.ReducePrompt,
		})
	}
	if err != nil {
		e.failRPC(ctx, job.ID, err)
		return
	}
	for {
		event, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			current, currentErr := e.store.AIJob(job.ID)
			if currentErr == nil && current.State == model.AIJobCanceled {
				return
			}
			e.failRPC(ctx, job.ID, recvErr)
			return
		}
		if progress := event.GetProgress(); progress != nil {
			if err := e.store.UpdateAIJobProgress(job.ID, progress.Stage, int(progress.Percent)); err == nil {
				e.events.Publish(TopicAIJobs)
			}
		}
		if result := event.GetTranscription(); result != nil {
			if err := e.store.WithContext(ctx).FinishTranscription(job.ID, transcriptionModel(result)); err != nil {
				e.logger.ErrorContext(ctx, "persisting transcription failed", "event", "ai.job.persist_failed", "job_id", job.ID, "error", err)
			}
			return
		}
		if result := event.GetSummary(); result != nil {
			var usage json.RawMessage
			if result.Usage != nil {
				usage, _ = result.Usage.MarshalJSON()
			}
			if err := e.store.WithContext(ctx).FinishSummary(job.ID, model.AISummaryResult{Markdown: result.Markdown, Usage: usage}); err != nil {
				e.logger.ErrorContext(ctx, "persisting summary failed", "event", "ai.job.persist_failed", "job_id", job.ID, "error", err)
			}
			return
		}
	}
	e.fail(ctx, job.ID, "worker_incomplete", "AI Worker 未返回结果")
}

func profileProto(profile model.AIProfile) *aiworkerpb.ProviderConfig {
	return &aiworkerpb.ProviderConfig{
		BaseUrl: profile.BaseURL, ApiKey: profile.APIKey, Model: profile.Model, Language: profile.Language, Prompt: profile.Prompt,
		Temperature: profile.Temperature, MaxOutputTokens: profile.MaxOutputTokens, ContextWindowChars: int32(profile.ContextWindowChars), TimeoutSec: int32(profile.TimeoutSec),
	}
}

func transcriptionModel(result *aiworkerpb.TranscriptionResult) model.AITranscriptionResult {
	pages := make([]model.AITranscriptPage, 0, len(result.Pages))
	for _, page := range result.Pages {
		segments := make([]model.AITranscriptSegment, 0, len(page.Segments))
		for _, segment := range page.Segments {
			segments = append(segments, model.AITranscriptSegment{StartMS: segment.StartMs, EndMS: segment.EndMs, Text: segment.Text})
		}
		pages = append(pages, model.AITranscriptPage{Page: int(page.Page), CID: page.Cid, Title: page.Title, DurationMS: page.DurationMs, Segments: segments})
	}
	var usage json.RawMessage
	if result.Usage != nil {
		usage, _ = result.Usage.MarshalJSON()
	}
	return model.AITranscriptionResult{BVID: result.Bvid, Title: result.Title, Pages: pages, Usage: usage}
}

func (e *AIEngine) fail(ctx context.Context, id, code, message string) {
	if err := e.store.WithContext(ctx).FailAIJob(id, code, message); err != nil && !errors.Is(err, state.ErrNotFound) {
		e.logger.Error("failing AI job failed", "event", "ai.job.fail_failed", "job_id", id, "error", err)
	}
}

func (e *AIEngine) failRPC(ctx context.Context, id string, err error) {
	code, message := "worker_lost", "AI Worker 连接中断"
	if current, ok := status.FromError(err); ok {
		var detail struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(current.Message()), &detail) == nil {
			if strings.TrimSpace(detail.Code) != "" {
				code = detail.Code
			}
			if strings.TrimSpace(detail.Message) != "" {
				message = detail.Message
			}
		}
	}
	e.fail(ctx, id, code, message)
}
