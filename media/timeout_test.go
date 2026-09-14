package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureTimeoutDoesNotBlockOtherImages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		flushHeaders bool
	}{
		{name: "stalled headers"}, {name: "stalled response body", flushHeaders: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/good" {
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(testPNG)
					return
				}
				if tt.flushHeaders {
					w.Header().Set("Content-Type", "image/png")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			downloader := &Downloader{DataDir: t.TempDir(), Client: server.Client(), AllowPrivateNetwork: true, Timeout: 100 * time.Millisecond}
			dynamic := model.Dynamic{ID: "1", UID: "42", Media: []model.DynamicMedia{{URL: server.URL + "/stall"}, {URL: server.URL + "/good"}}}
			type result struct{ good, bad int }
			done := make(chan result, 1)
			go func() { good, bad, _ := downloader.Ensure(ctx, &dynamic); done <- result{good, bad} }()
			select {
			case got := <-done:
				assert.Equal(t, 1, got.good)
				assert.Equal(t, 1, got.bad)
				assert.Empty(t, dynamic.Media[0].LocalPath)
				assert.Equal(t, server.URL+"/stall", dynamic.Media[0].URL)
				require.NotEmpty(t, dynamic.Media[1].LocalPath)
			case <-time.After(3 * time.Second):
				cancel()
				<-done
				t.Fatal("image download did not respect its deadline")
			}
		})
	}
}
