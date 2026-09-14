package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type sessionLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *sessionLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}
func (*sessionLogExporter) Shutdown(context.Context) error   { return nil }
func (*sessionLogExporter) ForceFlush(context.Context) error { return nil }

func TestSessionMaintenanceTelemetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, result   string
		requests       int
		requestFailure bool
	}{
		{name: "not due", result: "not_due", requests: 2},
		{name: "renew", result: "renewed", requests: 6},
		{name: "resume confirmation", result: "confirmed", requests: 2},
		{name: "confirmation failure", result: "error", requests: 6, requestFailure: true},
		{name: "business failure", result: "requires_login", requests: 2, requestFailure: true},
		{name: "schema failure", result: "error", requests: 2, requestFailure: true},
		{name: "csrf schema failure", result: "error", requests: 3, requestFailure: true},
		{name: "transport status failure", result: "error", requests: 1, requestFailure: true},
		{name: "missing token", result: "requires_login", requests: 1},
		{name: "paused", result: "skipped"},
		{name: "canceled", result: "canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Save the actual randomized path so telemetry can be checked for leaks.
			var secretPath atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/nav"):
					if tt.name == "transport status failure" {
						w.WriteHeader(503)
						return
					}
					_, _ = io.WriteString(w, `{"code":0,"data":{"isLogin":true,"mid":42,"uname":"User"}}`)
				case strings.HasSuffix(r.URL.Path, "/cookie/info"):
					if tt.name == "business failure" {
						_, _ = io.WriteString(w, `{"code":86095,"message":"private-upstream-body"}`)
						return
					}
					if tt.name == "schema failure" {
						_, _ = io.WriteString(w, `{"code":0,"data":"private-upstream-body"}`)
						return
					}
					_, _ = fmt.Fprintf(w, `{"code":0,"data":{"refresh":%t,"timestamp":123}}`, tt.name == "renew" || tt.name == "confirmation failure" || tt.name == "csrf schema failure")
				case strings.HasPrefix(r.URL.Path, "/correspond/"):
					secretPath.Store(r.URL.Path)
					if tt.name == "csrf schema failure" {
						_, _ = io.WriteString(w, `<div>private-upstream-body</div>`)
						return
					}
					_, _ = io.WriteString(w, `<div id="1-name">private-csrf</div>`)
				case strings.HasSuffix(r.URL.Path, "/cookie/refresh"):
					http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "private-cookie"})
					http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "private-jct"})
					_, _ = io.WriteString(w, `{"code":0,"data":{"refresh_token":"private-refresh-token"}}`)
				case strings.HasSuffix(r.URL.Path, "/confirm/refresh"):
					if tt.name == "confirmation failure" {
						w.WriteHeader(503)
						return
					}
					_, _ = io.WriteString(w, `{"code":0}`)
				default:
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(server.Close)
			spans := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
			reader := sdkmetric.NewManualReader()
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			exporter := new(sessionLogExporter)
			lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
			t.Cleanup(func() {
				require.NoError(t, tp.Shutdown(context.Background()))
				require.NoError(t, mp.Shutdown(context.Background()))
				require.NoError(t, lp.Shutdown(context.Background()))
			})
			var stdout bytes.Buffer
			loggers, err := logging.Open(logging.Config{Level: "debug", Stdout: &stdout, Provider: lp})
			require.NoError(t, err)
			engine := renewalEngine(t, server)
			client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL), bilibili.WithWebURL(server.URL), bilibili.WithTelemetry(tp, mp))
			engine.client = client
			engine.sessionClient = bilibili.NewSessionClient(client)
			engine.tracer = tp.Tracer(metricsInstrumentationName)
			engine.metrics = NewMetrics(mp)
			engine.logger = loggers.System
			if tt.name == "paused" {
				engine.sessionRetryAt = time.Now().Add(time.Hour)
			}
			if tt.name == "missing token" || tt.name == "resume confirmation" {
				session, err := engine.store.Session()
				require.NoError(t, err)
				if tt.name == "missing token" {
					session.RefreshToken = ""
				} else {
					session.PendingRefreshToken = "private-pending-token"
				}
				require.NoError(t, engine.store.SaveSession(session))
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tt.name == "canceled" {
				cancel()
			}
			engine.maintainBiliSession(ctx)
			ended := spans.Ended()
			require.Len(t, ended, tt.requests+1)
			root := ended[len(ended)-1]
			assert.Equal(t, "bilibili.session.maintain", root.Name())
			values := attribute.NewSet(root.Attributes()...)
			value, _ := values.Value("result")
			assert.Equal(t, tt.result, value.AsString())
			if tt.result == "error" || tt.result == "requires_login" {
				assert.Equal(t, codes.Error, root.Status().Code)
			} else {
				assert.NotEqual(t, codes.Error, root.Status().Code)
			}
			var failed bool
			for _, span := range ended[:len(ended)-1] {
				assert.Equal(t, trace.SpanKindClient, span.SpanKind())
				assert.Equal(t, root.SpanContext().TraceID(), span.SpanContext().TraceID())
				assert.Equal(t, root.SpanContext().SpanID(), span.Parent().SpanID())
				failed = failed || span.Status().Code == codes.Error
			}
			assert.Equal(t, tt.requestFailure, failed)
			var collected metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(t.Context(), &collected))
			var requestCount int64
			var runs int64
			for _, scope := range collected.ScopeMetrics {
				for _, measurement := range scope.Metrics {
					switch measurement.Name {
					case "bili_notify.workflow.runs":
						sum := measurement.Data.(metricdata.Sum[int64])
						require.Len(t, sum.DataPoints, 1)
						point := sum.DataPoints[0]
						runs += point.Value
						assert.Equal(t, attribute.NewSet(attribute.String("platform", "bilibili"), attribute.String("workflow", "session_maintenance"), attribute.String("result", tt.result)), point.Attributes)
					case "bili_notify.workflow.duration":
						histogram := measurement.Data.(metricdata.Histogram[float64])
						require.Len(t, histogram.DataPoints, 1)
						assert.EqualValues(t, 1, histogram.DataPoints[0].Count)
					case "bili_notify.bilibili.requests":
						for _, point := range measurement.Data.(metricdata.Sum[int64]).DataPoints {
							requestCount += point.Value
							assert.LessOrEqual(t, point.Attributes.Len(), 3)
						}
					}
				}
			}
			assert.EqualValues(t, 1, runs)
			assert.EqualValues(t, tt.requests, requestCount)
			assert.Zero(t, engine.metrics.lastWorkflowSuccess.Load(), "renewal must not hide a stalled collector")
			var complete bool
			exporter.mu.Lock()
			for _, record := range exporter.records {
				attrs := map[string]string{}
				record.WalkAttributes(func(a attribute.KeyValue) bool { attrs[string(a.Key)] = a.Value.AsString(); return true })
				if strings.HasPrefix(attrs["event"], "bilibili.session.") {
					assert.Equal(t, root.SpanContext().TraceID(), record.TraceID())
					assert.Equal(t, root.SpanContext().SpanID(), record.SpanID())
				}
				if attrs["event"] == "bilibili.session.maintenance_completed" {
					complete = true
					assert.Equal(t, tt.result, attrs["result"])
				}
			}
			exporter.mu.Unlock()
			assert.True(t, complete)
			rendered := stdout.String() + fmt.Sprint(collected)
			for _, span := range ended {
				rendered += span.Name() + fmt.Sprint(span.Attributes(), span.Events(), span.Status())
			}
			assert.NotContains(t, rendered, "private-")
			if path := secretPath.Load(); path != nil {
				assert.NotContains(t, rendered, path.(string))
			}
		})
	}
}
