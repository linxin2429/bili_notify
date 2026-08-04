package service

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPollUPBuildsBaselineThenOutbox(t *testing.T) {
	var request atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := request.Add(1)
		items := dynamicFixture("1", 1700000000)
		if count > 1 {
			items += "," + dynamicFixture("2", 1700000001)
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, items)
	}))
	defer server.Close()

	v, err := vault.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	up := model.UP{UID: "42", Enabled: true}
	if err := store.PutUP(up); err != nil {
		t.Fatal(err)
	}
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(prometheus.NewRegistry()), testSettings(30, 10, 1), nil)
	if err := engine.pollUP(t.Context(), up, []string{"channel"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ListDeliveries(0); len(got) != 0 {
		t.Fatalf("baseline created %d deliveries", len(got))
	}
	up, err = store.UP("42")
	if err != nil || !up.BaselineReady {
		t.Fatalf("UP after baseline=%#v err=%v", up, err)
	}
	if err := engine.pollUP(t.Context(), up, []string{"channel"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListDeliveries(0)
	if err != nil || len(got) != 1 || got[0].Dynamic.ID != "2" {
		t.Fatalf("deliveries=%#v err=%v", got, err)
	}
}

func dynamicFixture(id string, timestamp int64) string {
	return fmt.Sprintf(`{"id_str":%q,"type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"tester","pub_ts":%d},"module_dynamic":{"desc":{"text":"hello"},"major":null}}}`, id, timestamp)
}

func TestRetryDelayBounds(t *testing.T) {
	for range 100 {
		delay := retryDelay(0)
		if delay < 2500*time.Millisecond || delay >= 5*time.Second {
			t.Fatalf("retryDelay(0)=%s", delay)
		}
	}
}

func TestPollUPLogsSchemaFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"tester","pub_ts":"invalid"}}}]}}`)
	}))
	defer server.Close()

	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"), mustTestVault(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	up := model.UP{UID: "42", Name: "configured name", Enabled: true}
	if err := store.PutUP(up); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	engine := NewEngine(store, client, slog.New(slog.NewJSONHandler(&logs, nil)), NewMetrics(prometheus.NewRegistry()), testSettings(30, 10, 1), nil)
	if err := engine.pollUP(t.Context(), up, []string{"channel"}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UP(up.UID)
	if err != nil || updated.ConsecutiveFail != 1 {
		t.Fatalf("updated UP=%#v err=%v", updated, err)
	}
	for _, expected := range []string{`"msg":"Bilibili UP poll failed"`, `"uid":"42"`, `"up_name":"configured name"`, `"error_kind":"schema"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("log does not contain %s: %s", expected, logs.String())
		}
	}
}

func mustTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func testSettings(pollSec int, rate float64, concurrency int) model.RuntimeSettings {
	return model.RuntimeSettings{PollIntervalSec: pollSec, RequestRate: rate, RequestConcurrency: concurrency}
}

func TestUpdateSettingsHotReloadsAndPersists(t *testing.T) {
	t.Parallel()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	events := NewEventBus()
	sub := events.Subscribe()
	t.Cleanup(sub.Close)

	engine := NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(prometheus.NewRegistry()), testSettings(30, 2, 4), events)
	updated := testSettings(120, 1.5, 8)
	require.NoError(t, engine.UpdateSettings(updated))
	assert.Equal(t, updated, engine.Settings())

	loaded, err := store.RuntimeSettings()
	require.NoError(t, err)
	assert.Equal(t, updated, loaded)

	topics, _, err := sub.Next(t.Context())
	require.NoError(t, err)
	assert.NotZero(t, topics&TopicSettings)
	assert.NotZero(t, topics&TopicStatus)

	require.Error(t, engine.UpdateSettings(testSettings(5, 2, 4)))
	assert.Equal(t, updated, engine.Settings())
}

func TestStatusReadyUsesPollIntervalWindow(t *testing.T) {
	t.Parallel()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.PutUP(model.UP{UID: "1", Enabled: true, BaselineReady: true}))
	_, err = store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/hook"},
	})
	require.NoError(t, err)

	engine := NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(prometheus.NewRegistry()), testSettings(180, 2, 4), nil)
	engine.authValid.Store(true)
	engine.lastSuccess.Store(time.Now().Add(-150 * time.Second).Unix())

	status, err := engine.Status()
	require.NoError(t, err)
	assert.True(t, status.Ready, "150s lag should still be ready when poll_interval is 180s")

	engine.lastSuccess.Store(time.Now().Add(-7 * time.Minute).Unix())
	status, err = engine.Status()
	require.NoError(t, err)
	assert.False(t, status.Ready, "beyond 2*poll_interval should be unready")
}
