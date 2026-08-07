package bilibili

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestTelemetryDoesNotRecordRequestURLsOrQueryValues(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"message":"0","data":{"code":86101}}`))
	}))
	t.Cleanup(server.Close)
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() { require.NoError(t, tracerProvider.Shutdown(context.Background())) })
	metricReader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader))
	t.Cleanup(func() { require.NoError(t, meterProvider.Shutdown(context.Background())) })

	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL), WithTelemetry(tracerProvider, meterProvider))
	status, _, err := client.PollQR(t.Context(), "secret-query-value")
	require.NoError(t, err)
	assert.Equal(t, QRWaiting, status)

	spans := spanRecorder.Ended()
	require.Len(t, spans, 1)
	assert.Contains(t, spans[0].Name(), "/x/passport-login/web/qrcode/poll")
	for _, value := range spans[0].Attributes() {
		assert.NotEqual(t, "url.full", string(value.Key))
		assert.NotEqual(t, "url.query", string(value.Key))
		assert.False(t, strings.Contains(value.Value.Emit(), "secret-query-value"))
	}

	var data metricdata.ResourceMetrics
	require.NoError(t, metricReader.Collect(t.Context(), &data))
	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name != "bili_notify.bilibili.requests" {
				continue
			}
			sum := measurement.Data.(metricdata.Sum[int64])
			require.Len(t, sum.DataPoints, 1)
			for _, value := range sum.DataPoints[0].Attributes.ToSlice() {
				assert.NotEqual(t, "url.full", string(value.Key))
				assert.False(t, strings.Contains(value.Value.Emit(), "secret-query-value"))
			}
			return
		}
	}
	t.Fatal("Bilibili request metric was not collected")
}

func TestTelemetryDoesNotRecordURLFromTransportError(t *testing.T) {
	t.Parallel()
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() { require.NoError(t, tracerProvider.Shutdown(context.Background())) })
	metricReader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader))
	t.Cleanup(func() { require.NoError(t, meterProvider.Shutdown(context.Background())) })
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("request failed for %s", request.URL.String())
	})}

	client := New(httpClient, "test", WithBaseURLs("https://api.example.test", "https://passport.example.test"), WithTelemetry(tracerProvider, meterProvider))
	_, _, err := client.PollQR(t.Context(), "secret-query-value")
	require.Error(t, err)

	spans := spanRecorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "Bilibili operation failed", spans[0].Status().Description)
	assert.Empty(t, spans[0].Events())
	for _, value := range spans[0].Attributes() {
		assert.False(t, strings.Contains(value.Value.Emit(), "secret-query-value"))
	}
}
