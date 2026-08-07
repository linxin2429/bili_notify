package service

import (
	"context"
	"math"
	"sync/atomic"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const metricsInstrumentationName = "github.com/linxin2429/bili_notify/service"

// Metrics is the bounded-cardinality application metric contract.
type Metrics struct {
	workflowRuns       metric.Int64Counter
	workflowDuration   metric.Float64Histogram
	contentDiscovered  metric.Int64Counter
	discoveryDelay     metric.Float64Histogram
	commentPolls       metric.Int64Counter
	deliveryAttempts   metric.Int64Counter
	deliveryDuration   metric.Float64Histogram
	mediaDownloads     metric.Int64Counter
	mediaDownloadBytes metric.Int64Counter
	mediaMissing       metric.Int64Counter
	auditWriteFailures metric.Int64Counter

	lastWorkflowSuccess atomic.Int64
	outboxPending       atomic.Int64
	outboxBlocked       atomic.Int64
	oldestOutboxAgeBits atomic.Uint64
	authValid           atomic.Int64
	ready               atomic.Int64
	riskPaused          atomic.Int64
	upTotal             atomic.Int64
	upEnabled           atomic.Int64
	channelTotal        atomic.Int64
	channelEnabled      atomic.Int64
	commentTargets      atomic.Int64
	pollInterval        atomic.Int64
	requestRateBits     atomic.Uint64
	requestConcurrency  atomic.Int64
	deliveryConcurrency atomic.Int64
	backlogAlertCount   atomic.Int64
	backlogAlertAge     atomic.Int64
}

// NewMetrics creates all application instruments on provider.
func NewMetrics(provider metric.MeterProvider) *Metrics {
	meter := provider.Meter(metricsInstrumentationName)
	m := &Metrics{
		workflowRuns: must(meter.Int64Counter("bili_notify.workflow.runs",
			metric.WithDescription("Completed application workflow runs."))),
		workflowDuration: must(meter.Float64Histogram("bili_notify.workflow.duration",
			metric.WithDescription("Application workflow duration."), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60))),
		contentDiscovered: must(meter.Int64Counter("bili_notify.content.discovered",
			metric.WithDescription("Content records discovered and queued."))),
		discoveryDelay: must(meter.Float64Histogram("bili_notify.content.discovery_delay",
			metric.WithDescription("Delay between publication and discovery."), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(5, 10, 30, 60, 120, 300, 900, 3600))),
		commentPolls: must(meter.Int64Counter("bili_notify.comment.polls",
			metric.WithDescription("Comment target poll outcomes."))),
		deliveryAttempts: must(meter.Int64Counter("bili_notify.notification.delivery.attempts",
			metric.WithDescription("Notification delivery outcomes."))),
		deliveryDuration: must(meter.Float64Histogram("bili_notify.notification.delivery.duration",
			metric.WithDescription("Notification delivery duration."), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60))),
		mediaDownloads: must(meter.Int64Counter("bili_notify.media.downloads",
			metric.WithDescription("Dynamic media download outcomes."))),
		mediaDownloadBytes: must(meter.Int64Counter("bili_notify.media.download.bytes",
			metric.WithDescription("Bytes downloaded for dynamic media."), metric.WithUnit("By"))),
		mediaMissing: must(meter.Int64Counter("bili_notify.media.missing",
			metric.WithDescription("Expected local media files missing when used."))),
		auditWriteFailures: must(meter.Int64Counter("bili_notify.audit.write_failures",
			metric.WithDescription("Administrator audit records that could not be persisted."))),
	}

	must(meter.Int64ObservableGauge("bili_notify.workflow.last_success",
		metric.WithDescription("Unix timestamp of the last successful collection workflow."),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			observer.Observe(m.lastWorkflowSuccess.Load(), metric.WithAttributes(attribute.String("workflow", "collection")))
			return nil
		})))
	registerIntGauge(meter, "bili_notify.outbox.pending", "Pending delivery count.", &m.outboxPending)
	registerIntGauge(meter, "bili_notify.outbox.blocked", "Blocked delivery count.", &m.outboxBlocked)
	registerFloatGauge(meter, "bili_notify.outbox.oldest_age", "Age of the oldest delivery.", "s", &m.oldestOutboxAgeBits)
	registerIntGauge(meter, "bili_notify.auth.valid", "1 when the Bilibili session is valid.", &m.authValid)
	registerIntGauge(meter, "bili_notify.ready", "1 when collection and notification prerequisites are ready.", &m.ready)
	registerIntGauge(meter, "bili_notify.risk_paused", "1 while Bilibili risk control pauses collection.", &m.riskPaused)
	registerIntGauge(meter, "bili_notify.ups", "Configured UP count.", &m.upTotal)
	registerIntGauge(meter, "bili_notify.ups.enabled", "Enabled UP count.", &m.upEnabled)
	registerIntGauge(meter, "bili_notify.channels", "Configured notification channel count.", &m.channelTotal)
	registerIntGauge(meter, "bili_notify.channels.enabled", "Enabled notification channel count.", &m.channelEnabled)
	registerIntGauge(meter, "bili_notify.comment_targets", "Tracked comment target count.", &m.commentTargets)
	registerIntGauge(meter, "bili_notify.config.poll_interval", "Configured collection interval in seconds.", &m.pollInterval)
	registerFloatGauge(meter, "bili_notify.config.request_rate", "Configured Bilibili request rate.", "{request}/s", &m.requestRateBits)
	registerIntGauge(meter, "bili_notify.config.request_concurrency", "Configured Bilibili request concurrency.", &m.requestConcurrency)
	registerIntGauge(meter, "bili_notify.config.delivery_concurrency", "Configured delivery concurrency.", &m.deliveryConcurrency)
	registerIntGauge(meter, "bili_notify.config.backlog_alert_count", "Configured outbox count alert threshold.", &m.backlogAlertCount)
	registerIntGauge(meter, "bili_notify.config.backlog_alert_age", "Configured outbox age alert threshold in seconds.", &m.backlogAlertAge)
	return m
}

func (m *Metrics) RecordWorkflow(ctx context.Context, workflow, result string, duration time.Duration) {
	attrs := metric.WithAttributes(attribute.String("workflow", workflow), attribute.String("result", result))
	m.workflowRuns.Add(ctx, 1, attrs)
	m.workflowDuration.Record(ctx, duration.Seconds(), attrs)
	if result == "success" && workflow == "collection" {
		m.lastWorkflowSuccess.Store(time.Now().Unix())
	}
}

func (m *Metrics) RecordContent(ctx context.Context, kind string, count int, publishedAt, discoveredAt time.Time) {
	if count > 0 {
		m.contentDiscovered.Add(ctx, int64(count), metric.WithAttributes(attribute.String("content.type", kind)))
	}
	if !publishedAt.IsZero() {
		m.discoveryDelay.Record(ctx, max(0, discoveredAt.Sub(publishedAt).Seconds()),
			metric.WithAttributes(attribute.String("content.type", kind)))
	}
}

func (m *Metrics) RecordCommentPoll(ctx context.Context, result string, discovered int) {
	m.commentPolls.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	if discovered > 0 {
		m.contentDiscovered.Add(ctx, int64(discovered), metric.WithAttributes(attribute.String("content.type", "comment")))
	}
}

func (m *Metrics) RecordDelivery(ctx context.Context, channelType, result string, duration time.Duration) {
	attrs := metric.WithAttributes(attribute.String("notification.channel.type", channelType), attribute.String("result", result))
	m.deliveryAttempts.Add(ctx, 1, attrs)
	m.deliveryDuration.Record(ctx, duration.Seconds(), attrs)
}

func (m *Metrics) SetOutbox(deliveries []model.Delivery, age time.Duration) {
	var pending, blocked int64
	for _, delivery := range deliveries {
		if delivery.State == model.DeliveryBlocked {
			blocked++
		} else {
			pending++
		}
	}
	m.outboxPending.Store(pending)
	m.outboxBlocked.Store(blocked)
	m.oldestOutboxAgeBits.Store(math.Float64bits(max(0, age.Seconds())))
}

func (m *Metrics) SetAuth(valid bool) { m.authValid.Store(boolInt64(valid)) }

func (m *Metrics) SetStatus(ready, riskPaused bool, upTotal, upEnabled, channelTotal, channelEnabled, commentTargets int) {
	m.ready.Store(boolInt64(ready))
	m.riskPaused.Store(boolInt64(riskPaused))
	m.upTotal.Store(int64(upTotal))
	m.upEnabled.Store(int64(upEnabled))
	m.channelTotal.Store(int64(channelTotal))
	m.channelEnabled.Store(int64(channelEnabled))
	m.commentTargets.Store(int64(commentTargets))
}

func (m *Metrics) ApplySettings(settings model.RuntimeSettings) {
	m.pollInterval.Store(int64(settings.PollIntervalSec))
	m.requestRateBits.Store(math.Float64bits(settings.RequestRate))
	m.requestConcurrency.Store(int64(settings.RequestConcurrency))
	m.deliveryConcurrency.Store(int64(settings.DeliveryConcurrency))
	m.backlogAlertCount.Store(int64(settings.BacklogAlertCount))
	m.backlogAlertAge.Store(int64(settings.BacklogAlertAgeSec))
}

func (m *Metrics) RecordMediaDownloads(ctx context.Context, succeeded, failed int) {
	if succeeded > 0 {
		m.mediaDownloads.Add(ctx, int64(succeeded), metric.WithAttributes(attribute.String("result", "success")))
	}
	if failed > 0 {
		m.mediaDownloads.Add(ctx, int64(failed), metric.WithAttributes(attribute.String("result", "error")))
	}
}

func (m *Metrics) AddMediaBytes(ctx context.Context, bytes int64) {
	if bytes > 0 {
		m.mediaDownloadBytes.Add(ctx, bytes)
	}
}

func (m *Metrics) RecordMediaMissing(ctx context.Context)      { m.mediaMissing.Add(ctx, 1) }
func (m *Metrics) RecordAuditWriteFailure(ctx context.Context) { m.auditWriteFailures.Add(ctx, 1) }

func registerIntGauge(meter metric.Meter, name, description string, value *atomic.Int64) {
	must(meter.Int64ObservableGauge(name, metric.WithDescription(description),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			observer.Observe(value.Load())
			return nil
		})))
}

func registerFloatGauge(meter metric.Meter, name, description, unit string, value *atomic.Uint64) {
	must(meter.Float64ObservableGauge(name, metric.WithDescription(description), metric.WithUnit(unit),
		metric.WithFloat64Callback(func(_ context.Context, observer metric.Float64Observer) error {
			observer.Observe(math.Float64frombits(value.Load()))
			return nil
		})))
}

func must[T any](instrument T, err error) T {
	if err != nil {
		panic(err)
	}
	return instrument
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
