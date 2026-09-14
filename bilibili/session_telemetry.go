package bilibili

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// observeRequest reuses the existing Bilibili request instruments. Completion
// includes response decoding, so HTTP 200 business/schema failures count as
// errors. Only bounded route templates and classifications become attributes.
func (c *SessionClient) observeRequest(ctx context.Context, operation string) (context.Context, func(*http.Response, error)) {
	ctx, span := c.client.tracer.Start(ctx, "bilibili "+operation, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("bilibili.operation", operation)))
	started := time.Now()
	return ctx, func(response *http.Response, err error) {
		result := "success"
		if err != nil {
			result = "error"
			if errors.Is(err, context.Canceled) {
				result = "canceled"
			} else {
				span.SetStatus(codes.Error, "Bilibili session request failed")
			}
			if apiErr, ok := errors.AsType[*APIError](err); ok {
				span.SetAttributes(attribute.String("error.kind", string(apiErr.Kind)), attribute.Int("bilibili.code", apiErr.Code))
			} else if errors.Is(err, ErrRefreshRejected) {
				span.SetAttributes(attribute.String("error.kind", "refresh_rejected"))
			}
		}
		attrs := []attribute.KeyValue{attribute.String("bilibili.operation", operation), attribute.String("result", result)}
		if response != nil {
			attrs = append(attrs, attribute.Int("http.response.status_code", response.StatusCode))
		}
		span.SetAttributes(attrs...)
		options := metric.WithAttributes(attrs...)
		c.client.requests.Add(ctx, 1, options)
		c.client.duration.Record(ctx, time.Since(started).Seconds(), options)
		span.End()
	}
}
