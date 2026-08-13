package requestgate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestGateCapsAllConcurrentRequests(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	gate := New(roundTripFunc(func(*http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release
		return testResponse(), nil
	}), 1000, 2, time.Second)

	results := make(chan error, 3)
	for range 3 {
		go func() {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
			if err == nil {
				response, roundTripErr := gate.RoundTrip(request)
				if roundTripErr == nil {
					roundTripErr = response.Body.Close()
				}
				err = roundTripErr
			}
			results <- err
		}()
	}

	requireReceive(t, started)
	requireReceive(t, started)
	assert.Never(t, func() bool { return len(started) != 0 }, 30*time.Millisecond, time.Millisecond)
	release <- struct{}{}
	requireReceive(t, started)
	release <- struct{}{}
	release <- struct{}{}
	for range 3 {
		require.NoError(t, <-results)
	}
	assert.Zero(t, gate.InFlight())
}

func TestGateUpdateDrainsBeforeReplacingLimit(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	gate := New(roundTripFunc(func(*http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release
		return testResponse(), nil
	}), 1000, 2, time.Second)

	firstDone := runRequest(t, gate)
	requireReceive(t, started)
	updated := make(chan error, 1)
	go func() { updated <- gate.Update(t.Context(), 1000, 1, time.Second) }()
	assert.Never(t, func() bool { return len(updated) != 0 }, 30*time.Millisecond, time.Millisecond)
	release <- struct{}{}
	require.NoError(t, <-firstDone)
	require.NoError(t, <-updated)

	secondDone := runRequest(t, gate)
	thirdDone := runRequest(t, gate)
	requireReceive(t, started)
	assert.Never(t, func() bool { return len(started) != 0 }, 30*time.Millisecond, time.Millisecond)
	release <- struct{}{}
	requireReceive(t, started)
	release <- struct{}{}
	require.NoError(t, <-secondDone)
	require.NoError(t, <-thirdDone)
}

func TestGateHoldsConcurrencyUntilResponseBodyCloses(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 2)
	gate := New(roundTripFunc(func(*http.Request) (*http.Response, error) {
		started <- struct{}{}
		return testResponse(), nil
	}), 1000, 1, time.Second)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	first, err := gate.RoundTrip(request)
	require.NoError(t, err)
	assert.Equal(t, 1, gate.InFlight())

	secondDone := runRequest(t, gate)
	assert.Never(t, func() bool { return len(started) == 2 }, 30*time.Millisecond, time.Millisecond)
	require.NoError(t, first.Body.Close())
	requireReceive(t, started)
	require.NoError(t, <-secondDone)
	assert.Zero(t, gate.InFlight())
}

func TestGateAdmissionCancellationDoesNotReachTransport(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	var calls atomic.Int64
	gate := New(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return testResponse(), nil
	}), 1000, 1, time.Second)

	firstDone := runRequest(t, gate)
	requireReceive(t, started)
	ctx, cancel := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	secondDone := make(chan error, 1)
	go func() {
		_, roundTripErr := gate.RoundTrip(request)
		secondDone <- roundTripErr
	}()
	cancel()
	require.ErrorIs(t, <-secondDone, context.Canceled)
	assert.Equal(t, int64(1), calls.Load())
	release <- struct{}{}
	require.NoError(t, <-firstDone)
}

func TestGatePauseAndTimeoutHonorContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		configure func(*Gate)
		base      roundTripFunc
		deadline  time.Duration
	}{
		{
			name:      "pause blocks admission to transport",
			configure: func(gate *Gate) { gate.PauseUntil(time.Now().Add(time.Second)) },
			base:      func(*http.Request) (*http.Response, error) { return testResponse(), nil },
			deadline:  20 * time.Millisecond,
		},
		{
			name:      "request timeout cancels transport",
			configure: func(*Gate) {},
			base: func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			},
			deadline: time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			gate := New(roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				return tt.base(request)
			}), 1000, 1, 15*time.Millisecond)
			tt.configure(gate)
			ctx, cancel := context.WithTimeout(t.Context(), tt.deadline)
			t.Cleanup(cancel)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
			require.NoError(t, err)
			_, err = gate.RoundTrip(request)
			require.Error(t, err)
			assert.True(t, errors.Is(err, context.DeadlineExceeded))
			if strings.Contains(tt.name, "pause") {
				assert.Zero(t, calls.Load())
			} else {
				assert.Equal(t, int64(1), calls.Load())
			}
		})
	}
}

func runRequest(t *testing.T, gate *Gate) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
		if err == nil {
			response, roundTripErr := gate.RoundTrip(request)
			if roundTripErr == nil {
				roundTripErr = response.Body.Close()
			}
			err = roundTripErr
		}
		done <- err
	}()
	return done
}

func requireReceive(t *testing.T, values <-chan struct{}) {
	t.Helper()
	select {
	case <-values:
	case <-time.After(time.Second):
		require.FailNow(t, "timed out waiting for request")
	}
}

func testResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}
}
