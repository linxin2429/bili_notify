package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestDatabaseSpanInheritsContextWithoutQueryValues(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 42), provider)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.PutUP(model.UP{UID: "4242424242", Name: "UP"}))
	assert.Empty(t, recorder.Ended(), "database operations without a parent must not create root spans")

	ctx, parent := provider.Tracer("test").Start(t.Context(), "workflow")
	_, err = store.WithContext(ctx).UP("4242424242")
	require.NoError(t, err)
	parentContext := parent.SpanContext()
	parent.End()

	var databaseSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Parent().SpanID() == parentContext.SpanID() {
			databaseSpan = span
			break
		}
	}
	require.NotNil(t, databaseSpan)
	assert.Equal(t, parentContext.TraceID(), databaseSpan.SpanContext().TraceID())
	for _, value := range databaseSpan.Attributes() {
		assert.NotContains(t, value.Value.Emit(), "4242424242")
	}
}

func TestDeliveryOriginTraceparentPersistsProducerContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		withParent bool
		enqueue    func(*Store) (int, error)
	}{
		{
			name:       "space dynamic",
			withParent: true,
			enqueue: func(store *Store) (int, error) {
				return store.RecordDynamics("42", []model.Dynamic{{
					ID: "space-dynamic", UID: "42", PublishedAt: time.Now(),
				}}, []string{"channel"}, DynamicBaselineNone)
			},
		},
		{
			name:       "aggregate feed dynamic",
			withParent: true,
			enqueue: func(store *Store) (int, error) {
				return store.RecordFeedDynamics("account", "baseline", []model.Dynamic{{
					ID: "feed-dynamic", UID: "42", PublishedAt: time.Now(),
				}}, []string{"channel"}, nil)
			},
		},
		{
			name:       "comment notification",
			withParent: true,
			enqueue: func(store *Store) (int, error) {
				return store.RecordCommentNotifications(model.CommentTarget{UID: "42"}, []model.CommentNotification{{
					RPID: "reply", UPUID: "42", PublishedAt: time.Now(),
				}}, []string{"channel"}, false)
			},
		},
		{
			name: "producer without span",
			enqueue: func(store *Store) (int, error) {
				return store.RecordDynamics("42", []model.Dynamic{{
					ID: "untraced-dynamic", UID: "42", PublishedAt: time.Now(),
				}}, []string{"channel"}, DynamicBaselineNone)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := sdktrace.NewTracerProvider()
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
			store, err := Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustVault(t, 43), provider)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })

			producerStore := store
			var producerContext trace.SpanContext
			if tt.withParent {
				ctx, producer := provider.Tracer("test").Start(t.Context(), "producer")
				producerContext = producer.SpanContext()
				producerStore = store.WithContext(ctx)
				t.Cleanup(func() { producer.End() })
			}
			created, err := tt.enqueue(producerStore)
			require.NoError(t, err)
			require.Equal(t, 1, created)
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			require.Len(t, deliveries, 1)

			if !tt.withParent {
				assert.Empty(t, deliveries[0].OriginTraceparent)
				return
			}
			originTraceparent := deliveries[0].OriginTraceparent
			origin := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{
				"traceparent": originTraceparent,
			})
			persistedContext := trace.SpanContextFromContext(origin)
			require.True(t, persistedContext.IsValid())
			assert.Equal(t, producerContext.TraceID(), persistedContext.TraceID())
			assert.Equal(t, producerContext.SpanID(), persistedContext.SpanID())

			require.NoError(t, store.FailDelivery(deliveries[0].ID, false, time.Now(), errors.New("retry"), nil))
			deliveries, err = store.ListDeliveries(0)
			require.NoError(t, err)
			require.Len(t, deliveries, 1)
			assert.Equal(t, originTraceparent, deliveries[0].OriginTraceparent)
		})
	}
}
