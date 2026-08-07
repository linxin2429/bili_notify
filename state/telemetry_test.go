package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
