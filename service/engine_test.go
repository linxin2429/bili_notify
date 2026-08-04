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
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(prometheus.NewRegistry()), 30*time.Second, 100, 1, nil)
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
	engine := NewEngine(store, client, slog.New(slog.NewJSONHandler(&logs, nil)), NewMetrics(prometheus.NewRegistry()), 30*time.Second, 100, 1, nil)
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
