package service

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(prometheus.NewRegistry()), 30*time.Second, 100, 1)
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
