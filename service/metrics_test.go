package service

import (
	"context"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetricsContract(t *testing.T) {
	t.Parallel()
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	metrics := NewMetrics(provider)

	settings := model.DefaultRuntimeSettings()
	metrics.ApplySettings(settings)
	metrics.RecordWorkflow(t.Context(), "collection", "success", 250*time.Millisecond)
	metrics.RecordContent(t.Context(), "dynamic", 2, time.Now().Add(-30*time.Second), time.Now())
	metrics.RecordDelivery(t.Context(), string(model.ChannelWeCom), "success", 500*time.Millisecond)
	metrics.SetOutbox([]model.Delivery{{State: model.DeliveryPending}, {State: model.DeliveryBlocked}}, 2*time.Minute)
	metrics.SetAuth(true)
	metrics.SetStatus(true, false, 3, 2, 2, 1, 4)

	var resourceMetrics metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &resourceMetrics))
	measurements := make(map[string]metricdata.Metrics)
	for _, scope := range resourceMetrics.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			measurements[measurement.Name] = measurement
		}
	}

	tests := []struct {
		name string
		kind string
	}{
		{name: "bili_notify.workflow.runs", kind: "sum"},
		{name: "bili_notify.workflow.duration", kind: "histogram"},
		{name: "bili_notify.content.discovered", kind: "sum"},
		{name: "bili_notify.content.discovery_delay", kind: "histogram"},
		{name: "bili_notify.notification.delivery.attempts", kind: "sum"},
		{name: "bili_notify.notification.delivery.duration", kind: "histogram"},
		{name: "bili_notify.outbox.pending", kind: "gauge"},
		{name: "bili_notify.outbox.blocked", kind: "gauge"},
		{name: "bili_notify.ready", kind: "gauge"},
		{name: "bili_notify.config.poll_interval", kind: "gauge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			measurement, ok := measurements[tt.name]
			require.True(t, ok)
			switch tt.kind {
			case "sum":
				_, ok = measurement.Data.(metricdata.Sum[int64])
			case "histogram":
				_, ok = measurement.Data.(metricdata.Histogram[float64])
			case "gauge":
				_, intOK := measurement.Data.(metricdata.Gauge[int64])
				_, floatOK := measurement.Data.(metricdata.Gauge[float64])
				ok = intOK || floatOK
			}
			assert.True(t, ok, "unexpected aggregation type %T", measurement.Data)
		})
	}

	delivery := measurements["bili_notify.notification.delivery.attempts"].Data.(metricdata.Sum[int64])
	require.Len(t, delivery.DataPoints, 1)
	attributes := delivery.DataPoints[0].Attributes.ToSlice()
	keys := make([]string, 0, len(attributes))
	for _, value := range attributes {
		keys = append(keys, string(value.Key))
	}
	assert.ElementsMatch(t, []string{"notification.channel.type", "result"}, keys)
	for _, forbidden := range []string{"channel_id", "delivery_id", "error", "url.full"} {
		assert.NotContains(t, keys, forbidden)
	}

	duration := measurements["bili_notify.workflow.duration"].Data.(metricdata.Histogram[float64])
	require.Len(t, duration.DataPoints, 1)
	assert.Equal(t, []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}, duration.DataPoints[0].Bounds)
}
