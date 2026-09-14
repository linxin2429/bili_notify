package bilibili

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOpusBusinessFailureIsVisibleInTelemetry(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":4101131,"message":"unavailable"}`))
	}))
	t.Cleanup(server.Close)
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })
	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL), WithTelemetry(tp, mp))
	client.SetSession(model.BiliSession{Cookies: map[string]string{"buvid3": "test-device"}})
	_, err := client.fetchOpusDetail(t.Context(), "131580584")
	apiErr, ok := errors.AsType[*APIError](err)
	require.True(t, ok)
	assert.Equal(t, 4101131, apiErr.Code)
	assert.Equal(t, http.StatusOK, apiErr.HTTPStatus)
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	attrs := make(map[string]string)
	for _, attr := range spans[0].Attributes() {
		attrs[string(attr.Key)] = attr.Value.Emit()
	}
	assert.Equal(t, "4101131", attrs["bilibili.code"])
	assert.Equal(t, "200", attrs["http.response.status_code"])
	assert.Equal(t, "131580584", attrs["bilibili.opus.id"])
	assert.NotContains(t, attrs, "url.full")
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &data))
	var found bool
	for _, scope := range data.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name != "bili_notify.bilibili.requests" {
				continue
			}
			sum, ok := measurement.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			require.Len(t, sum.DataPoints, 1)
			value, ok := sum.DataPoints[0].Attributes.Value("result")
			require.True(t, ok)
			assert.Equal(t, "error", value.AsString())
			found = true
		}
	}
	assert.True(t, found)
}

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

func TestSessionTelemetrySanitizesTransportFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		canceled bool
	}{{name: "transport error"}, {name: "canceled", canceled: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			reader := sdkmetric.NewManualReader()
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() {
				require.NoError(t, tp.Shutdown(context.Background()))
				require.NoError(t, mp.Shutdown(context.Background()))
			})
			client := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, fmt.Errorf("private-transport %s", r.URL) })}, "test", WithTelemetry(tp, mp))
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tt.canceled {
				cancel()
			}
			_, err := NewSessionClient(client).Refresh(ctx, model.BiliSession{Cookies: map[string]string{"bili_jct": "private-csrf"}, RefreshToken: "private-token"}, 123)
			require.Error(t, err)
			ended := recorder.Ended()
			require.Len(t, ended, 1)
			assert.Equal(t, "bilibili /correspond/1/{correspondPath}", ended[0].Name())
			assert.Empty(t, ended[0].Events())
			if tt.canceled {
				assert.ErrorIs(t, err, context.Canceled)
				assert.NotEqual(t, codes.Error, ended[0].Status().Code)
			} else {
				assert.Equal(t, codes.Error, ended[0].Status().Code)
			}
			var metrics metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(t.Context(), &metrics))
			assert.NotContains(t, fmt.Sprint(metrics, ended[0].Attributes(), ended[0].Status()), "private-")
			for _, scope := range metrics.ScopeMetrics {
				for _, m := range scope.Metrics {
					if m.Name == "bili_notify.bilibili.requests" {
						sum := m.Data.(metricdata.Sum[int64])
						require.Len(t, sum.DataPoints, 1)
						assert.EqualValues(t, 1, sum.DataPoints[0].Value)
						value, ok := sum.DataPoints[0].Attributes.Value("result")
						require.True(t, ok)
						if tt.canceled {
							assert.Equal(t, "canceled", value.AsString())
						} else {
							assert.Equal(t, "error", value.AsString())
						}
					}
				}
			}
		})
	}
}
