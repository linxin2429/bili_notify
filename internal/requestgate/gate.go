package requestgate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Gate is a process-local request budget. Updates stop admission, drain all
// in-flight work, and only then publish the replacement limit.
type Gate struct {
	mu       sync.Mutex
	changed  chan struct{}
	active   int
	limit    int
	updating bool
	timeout  time.Duration
	limiter  *rate.Limiter
	pausedTo time.Time
	base     http.RoundTripper
}

func New(base http.RoundTripper, requestsPerSecond float64, concurrency int, timeout time.Duration) *Gate {
	if base == nil {
		base = http.DefaultTransport
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Gate{changed: make(chan struct{}), limit: concurrency, timeout: timeout,
		limiter: rate.NewLimiter(rate.Limit(requestsPerSecond), max(1, int(requestsPerSecond))), base: base}
}

func (g *Gate) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	if err := g.acquire(ctx); err != nil {
		return nil, err
	}
	defer g.release()

	g.mu.Lock()
	limiter, timeout, pausedTo := g.limiter, g.timeout, g.pausedTo
	g.mu.Unlock()
	if delay := time.Until(pausedTo); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err := limiter.Wait(ctx); err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	response, err := g.base.RoundTrip(request.Clone(requestCtx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &cancelBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

func (g *Gate) acquire(ctx context.Context) error {
	for {
		g.mu.Lock()
		if !g.updating && g.active < g.limit {
			g.active++
			g.mu.Unlock()
			return nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (g *Gate) release() {
	g.mu.Lock()
	g.active--
	g.signalLocked()
	g.mu.Unlock()
}

func (g *Gate) Update(ctx context.Context, requestsPerSecond float64, concurrency int, timeout time.Duration) error {
	if requestsPerSecond <= 0 || concurrency < 1 || timeout <= 0 {
		return errors.New("request gate settings must be positive")
	}
	g.mu.Lock()
	g.updating = true
	g.signalLocked()
	for g.active != 0 {
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			g.mu.Lock()
			g.updating = false
			g.signalLocked()
			g.mu.Unlock()
			return ctx.Err()
		case <-changed:
		}
		g.mu.Lock()
	}
	g.limit, g.timeout = concurrency, timeout
	g.limiter = rate.NewLimiter(rate.Limit(requestsPerSecond), max(1, int(requestsPerSecond)))
	g.updating = false
	g.signalLocked()
	g.mu.Unlock()
	return nil
}

func (g *Gate) PauseUntil(until time.Time) {
	g.mu.Lock()
	if until.After(g.pausedTo) {
		g.pausedTo = until
	}
	g.signalLocked()
	g.mu.Unlock()
}

func (g *Gate) InFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

func (g *Gate) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cancel)
	return err
}
