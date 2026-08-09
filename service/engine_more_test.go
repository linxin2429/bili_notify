package service

import (
	"context"
	"errors"
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
	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestDeliverClassifiesOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		status        int
		enabled       bool
		missing       bool
		wantChanged   bool
		wantRemaining bool
		wantState     model.DeliveryState
		wantAttempts  int
		wantSendSpan  bool
		wantSpanError bool
	}{
		{name: "success completes delivery", status: http.StatusOK, enabled: true, wantChanged: true, wantSendSpan: true},
		{name: "server failure schedules retry", status: http.StatusInternalServerError, enabled: true, wantChanged: true, wantRemaining: true, wantState: model.DeliveryPending, wantAttempts: 1, wantSendSpan: true, wantSpanError: true},
		{name: "client failure blocks delivery", status: http.StatusBadRequest, enabled: true, wantChanged: true, wantRemaining: true, wantState: model.DeliveryBlocked, wantAttempts: 1, wantSendSpan: true, wantSpanError: true},
		{name: "disabled channel leaves delivery alone", status: http.StatusOK, wantRemaining: true, wantState: model.DeliveryPending},
		{name: "missing channel blocks delivery", missing: true, wantChanged: true, wantRemaining: true, wantState: model.DeliveryBlocked, wantAttempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			webhook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
			}))
			t.Cleanup(webhook.Close)
			spanRecorder := tracetest.NewSpanRecorder()
			tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
			t.Cleanup(func() { require.NoError(t, tracerProvider.Shutdown(context.Background())) })
			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			channelID := "missing"
			if !tt.missing {
				channel, putErr := store.PutChannel(model.Channel{
					Name: "robot", Type: model.ChannelWeCom, Enabled: tt.enabled,
					Settings: map[string]string{"webhook": webhook.URL},
				})
				require.NoError(t, putErr)
				channelID = channel.ID
			}
			created, err := store.RecordDynamics("42", []model.Dynamic{{
				ID: "dynamic", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_WORD",
				PublishedAt: time.Now(), Summary: "hello", URL: "https://t.bilibili.com/1",
			}}, []string{channelID}, state.DynamicBaselineNone)
			require.NoError(t, err)
			require.Equal(t, 1, created)
			deliveries, err := store.ListDeliveries(0)
			require.NoError(t, err)
			require.Len(t, deliveries, 1)
			engine := NewEngine(
				store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)),
				NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil,
				WithNotificationHTTPClient(webhook.Client()),
				WithTracerProvider(tracerProvider),
			)

			changed, err := engine.deliver(t.Context(), deliveries[0])
			require.NoError(t, err)
			assert.Equal(t, tt.wantChanged, changed)
			var sendSpan sdktrace.ReadOnlySpan
			for _, span := range spanRecorder.Ended() {
				if span.Name() == "notification.send" {
					sendSpan = span
					break
				}
			}
			if tt.wantSendSpan {
				require.NotNil(t, sendSpan)
				if tt.wantSpanError {
					assert.Equal(t, codes.Error, sendSpan.Status().Code)
				} else {
					assert.Equal(t, codes.Unset, sendSpan.Status().Code)
				}
			} else {
				assert.Nil(t, sendSpan)
			}
			deliveries, err = store.ListDeliveries(0)
			require.NoError(t, err)
			if !tt.wantRemaining {
				assert.Empty(t, deliveries)
				return
			}
			require.Len(t, deliveries, 1)
			assert.Equal(t, tt.wantState, deliveries[0].State)
			assert.Equal(t, tt.wantAttempts, deliveries[0].Attempts)
			if tt.wantAttempts > 0 {
				assert.NotEmpty(t, deliveries[0].LastError)
			}
		})
	}
}

func TestDeliverContinuesPersistedProducerTrace(t *testing.T) {
	t.Parallel()
	webhook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	t.Cleanup(webhook.Close)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	channel, err := store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": webhook.URL},
	})
	require.NoError(t, err)

	producerCtx, producer := provider.Tracer("test").Start(t.Context(), "collection.poll_up")
	producerContext := producer.SpanContext()
	created, err := store.WithContext(producerCtx).RecordDynamics("42", []model.Dynamic{{
		ID: "dynamic", UID: "42", UPName: "UP", Type: "DYNAMIC_TYPE_WORD",
		PublishedAt: time.Now(), Summary: "hello", URL: "https://t.bilibili.com/1",
	}}, []string{channel.ID}, state.DynamicBaselineNone)
	require.NoError(t, err)
	require.Equal(t, 1, created)
	producer.End()
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.NotEmpty(t, deliveries[0].OriginTraceparent)

	engine := NewEngine(
		store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil,
		WithNotificationHTTPClient(webhook.Client()), WithTracerProvider(provider),
	)
	dispatchCtx, dispatch := provider.Tracer("test").Start(t.Context(), "delivery.dispatch")
	dispatchContext := dispatch.SpanContext()
	changed, err := engine.deliver(dispatchCtx, deliveries[0])
	require.NoError(t, err)
	assert.True(t, changed)
	dispatch.End()

	var deliverySpan, notificationSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		switch span.Name() {
		case "delivery.send":
			deliverySpan = span
		case "notification.send":
			notificationSpan = span
		}
	}
	require.NotNil(t, deliverySpan)
	require.NotNil(t, notificationSpan)
	assert.Equal(t, producerContext.TraceID(), deliverySpan.SpanContext().TraceID())
	assert.Equal(t, producerContext.SpanID(), deliverySpan.Parent().SpanID())
	assert.True(t, deliverySpan.Parent().IsRemote())
	assert.NotEqual(t, dispatchContext.TraceID(), deliverySpan.SpanContext().TraceID())
	assert.Empty(t, deliverySpan.Links())
	assert.Equal(t, deliverySpan.SpanContext().TraceID(), notificationSpan.SpanContext().TraceID())
	assert.Equal(t, deliverySpan.SpanContext().SpanID(), notificationSpan.Parent().SpanID())
}

func TestDeliveryOriginContextValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		traceparent string
		wantValid   bool
	}{
		{name: "valid", traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", wantValid: true},
		{name: "empty"},
		{name: "malformed", traceparent: "not-a-traceparent"},
		{name: "zero trace id", traceparent: "00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := sdktrace.NewTracerProvider()
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
			baseCtx, dispatch := provider.Tracer("test").Start(t.Context(), "dispatch")
			t.Cleanup(func() { dispatch.End() })
			baseContext := dispatch.SpanContext()
			cancelCtx, cancel := context.WithCancel(baseCtx)
			gotCtx, valid := deliveryOriginContext(cancelCtx, tt.traceparent)
			assert.Equal(t, tt.wantValid, valid)
			gotContext := trace.SpanContextFromContext(gotCtx)
			if tt.wantValid {
				assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotContext.TraceID().String())
				assert.True(t, gotContext.IsRemote())
			} else {
				assert.Equal(t, baseContext, gotContext)
			}
			cancel()
			assert.ErrorIs(t, gotCtx.Err(), context.Canceled)
		})
	}
}

func TestDispatchOnceDoesNotTraceIdleDatabasePolling(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t), provider)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	engine := NewEngine(
		store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil,
		WithTracerProvider(provider),
	)

	require.NoError(t, engine.dispatchOnce(t.Context()))
	assert.Empty(t, recorder.Ended())
}

func TestDeliveryMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		delivery    model.Delivery
		wantSubject string
		wantID      string
		wantErr     string
	}{
		{name: "dynamic", delivery: model.Delivery{Dynamic: model.Dynamic{ID: "dynamic", UPName: "UP", Type: "DYNAMIC_TYPE_WORD"}}, wantSubject: "UP", wantID: "dynamic"},
		{name: "comment", delivery: model.Delivery{Kind: model.DeliveryKindComment, Comment: &model.CommentNotification{RPID: "reply", UPName: "UP"}}, wantSubject: "UP", wantID: "reply"},
		{name: "missing comment", delivery: model.Delivery{Kind: model.DeliveryKindComment}, wantErr: "missing payload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			message, id, err := deliveryMessage(tt.delivery)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, message.Subject, tt.wantSubject)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func TestPollCommentTargetBaselinesThenQueuesNewReply(t *testing.T) {
	t.Parallel()
	var childRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/v2/reply":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"page":{"num":1,"size":20,"count":1},"replies":[{"rpid_str":"root","root_str":"0","parent_str":"0","ctime":1700000000,"rcount":2,"member":{"mid":"7","uname":"viewer"},"content":{"message":"root"}}]}}`)
		case "/x/v2/reply/reply":
			count := childRequests.Add(1)
			replies := `{"rpid_str":"reply-1","root_str":"root","parent_str":"root","ctime":1700000001,"member":{"mid":"42","uname":"UP"},"content":{"message":"first"}}`
			if count > 1 {
				replies += `,{"rpid_str":"reply-2","root_str":"root","parent_str":"reply-1","ctime":1700000002,"member":{"mid":"42","uname":"UP"},"content":{"message":"second"}}`
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"page":{"num":1,"size":20,"count":%d},"replies":[%s]}}`, count, replies)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	channel, err := store.PutChannel(model.Channel{
		Name: "robot", Type: model.ChannelWeCom, Enabled: true,
		Settings: map[string]string{"webhook": "https://example.com/hook"},
	})
	require.NoError(t, err)
	target := model.CommentTarget{
		UID: "42", UPName: "UP", DynamicID: "dynamic", ContentType: "DYNAMIC_TYPE_WORD",
		Title: "title", URL: "https://t.bilibili.com/1", CommentType: 11, CommentOID: "oid",
		PublishedAt: time.Unix(1700000000, 0),
	}
	require.NoError(t, store.PutCommentTargets("42", []model.CommentTarget{target}))
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)

	require.NoError(t, engine.pollCommentTarget(t.Context(), target, []string{channel.ID}, 1, 1))
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	assert.Empty(t, deliveries)
	targets, err := store.ListCommentTargets("42")
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.True(t, targets[0].BaselineReady)

	require.NoError(t, engine.pollCommentTarget(t.Context(), targets[0], []string{channel.ID}, 1, 1))
	deliveries, err = store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.NotNil(t, deliveries[0].Comment)
	assert.Equal(t, "reply-2", deliveries[0].Comment.RPID)
	require.Len(t, deliveries[0].Comment.Thread, 3)
	assert.Equal(t, "root", deliveries[0].Comment.Thread[0].RPID)
	assert.True(t, deliveries[0].Comment.Thread[2].IsTrigger)
}

func TestPollCommentTargetClosesUnavailableTarget(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":12002,"message":"closed","data":null}`)
	}))
	t.Cleanup(server.Close)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	target := model.CommentTarget{UID: "42", CommentType: 11, CommentOID: "oid"}
	require.NoError(t, store.PutCommentTargets("42", []model.CommentTarget{target}))
	engine := NewEngine(
		store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)),
		slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil,
	)
	require.NoError(t, engine.pollCommentTarget(t.Context(), target, []string{"channel"}, 1, 1))
	targets, err := store.ListCommentTargets("42")
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.True(t, targets[0].Closed)
	assert.Contains(t, targets[0].LastError, "closed")
}

func TestLoginLifecycle(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/passport-login/web/qrcode/generate":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"url":"https://example.invalid/login","qrcode_key":"key"}}`)
		case "/x/passport-login/web/qrcode/poll":
			http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "session", Path: "/"})
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"code":0}}`)
		case "/x/web-interface/nav":
			_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"isLogin":true,"mid":100,"uname":"account"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	engine := NewEngine(
		store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)),
		slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), NewEventBus(), nil,
	)
	runCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	engine.loginMu.Lock()
	engine.runCtx = runCtx
	engine.loginMu.Unlock()

	login, err := engine.StartLogin(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "key", login.Key)
	assert.Equal(t, bilibili.QRWaiting, login.Status)
	url, err := engine.LoginURL(login.Key)
	require.NoError(t, err)
	assert.Equal(t, "https://example.invalid/login", url)

	login, err = engine.PollLogin(t.Context(), login.Key)
	require.NoError(t, err)
	assert.Equal(t, bilibili.QRSuccess, login.Status)
	session, err := store.Session()
	require.NoError(t, err)
	assert.Equal(t, "100", session.AccountUID)
	assert.Equal(t, "account", session.AccountName)
	assert.True(t, engine.authValid.Load())

	engine.CancelLogin("other")
	_, ok := engine.Login()
	assert.True(t, ok)
	engine.CancelLogin(login.Key)
	_, ok = engine.Login()
	assert.False(t, ok)
	_, err = engine.LoginURL(login.Key)
	require.Error(t, err)
	_, err = engine.PollLogin(t.Context(), login.Key)
	require.Error(t, err)
	cancel()
	engine.loginWG.Wait()
}

func TestBuildCommentThreadEdgeCases(t *testing.T) {
	t.Parallel()
	target := model.CommentTarget{UID: "42"}
	tests := []struct {
		name           string
		trigger        bilibili.Reply
		roots          map[string]bilibili.Reply
		children       map[string]bilibili.Reply
		wantIDs        []string
		wantIncomplete bool
	}{
		{name: "complete parent chain", trigger: bilibili.Reply{RPID: "child", Root: "root", Parent: "root", Mid: "42"}, roots: map[string]bilibili.Reply{"root": {RPID: "root", Mid: "7"}}, wantIDs: []string{"root", "child"}},
		{name: "missing parent falls back to root", trigger: bilibili.Reply{RPID: "child", Root: "root", Parent: "missing", Mid: "42"}, roots: map[string]bilibili.Reply{"root": {RPID: "root", Mid: "7"}}, wantIDs: []string{"root", "child"}},
		{name: "missing root is incomplete", trigger: bilibili.Reply{RPID: "child", Root: "missing", Parent: "0", Mid: "42"}, wantIDs: []string{"child"}, wantIncomplete: true},
		{name: "cycle terminates", trigger: bilibili.Reply{RPID: "a", Parent: "b", Mid: "42"}, children: map[string]bilibili.Reply{"b": {RPID: "b", Parent: "a"}}, wantIDs: []string{"b", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nodes, incomplete := buildCommentThread(target, tt.trigger, tt.roots, tt.children)
			ids := make([]string, 0, len(nodes))
			for _, node := range nodes {
				ids = append(ids, node.RPID)
			}
			assert.Equal(t, tt.wantIDs, ids)
			assert.Equal(t, tt.wantIncomplete, incomplete)
			require.NotEmpty(t, nodes)
			assert.True(t, nodes[len(nodes)-1].IsTrigger)
		})
	}
}

func TestRefreshRelationsPersistsRouting(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/x/relation/relations", r.URL.Path)
		assert.Equal(t, "42,43", r.URL.Query().Get("fids"))
		_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"42":{"attribute":2},"43":{"attribute":0}}}`)
	}))
	t.Cleanup(server.Close)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	for _, up := range []model.UP{{UID: "42", Enabled: true}, {UID: "43", Enabled: true}} {
		require.NoError(t, store.PutUP(up))
	}
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	client.SetSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "session"}})
	events := NewEventBus()
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), events, nil)
	engine.authValid.Store(true)
	engine.setAccount(model.BiliAccount{UID: "100", Name: "account"})
	require.NoError(t, engine.refreshRelations(t.Context()))
	relations, err := store.FollowRelations("100")
	require.NoError(t, err)
	assert.Equal(t, model.Followed, relations["42"].State)
	assert.Equal(t, model.FollowUnfollowed, relations["43"].State)
	assert.Greater(t, events.Revision(), uint64(0))
}

func TestCollectOnceRoutesEnabledUP(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/x/polymer/web-dynamic/v1/feed/space", r.URL.Path)
		_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[%s]}}`, dynamicFixture("baseline", 1700000000))
	}))
	t.Cleanup(server.Close)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.PutUP(model.UP{UID: "42", Name: "UP", Enabled: true}))
	_, err = store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}})
	require.NoError(t, err)
	client := bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL))
	client.SetSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "session"}})
	engine := NewEngine(store, client, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil)
	engine.authValid.Store(true)
	engine.setAccount(model.BiliAccount{UID: "100", Name: "account"})
	require.NoError(t, engine.collectOnce(t.Context()))
	up, err := store.UP("42")
	require.NoError(t, err)
	assert.True(t, up.BaselineReady)
	assert.False(t, up.LastSuccessAt.IsZero())
	relations, err := store.FollowRelations("100")
	require.NoError(t, err)
	assert.True(t, relations["42"].SpaceSynced)
}

func TestInitializeFeed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		baseline      string
		updateNumJSON string
		wantErr       string
	}{
		{name: "stores baseline with numeric count", baseline: "cursor", updateNumJSON: `0`},
		{name: "stores baseline with quoted count", baseline: "cursor", updateNumJSON: `"0"`},
		{name: "rejects missing baseline", updateNumJSON: `0`, wantErr: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","update_baseline":%q,"update_num":%s,"items":[]}}`, tt.baseline, tt.updateNumJSON)
			}))
			t.Cleanup(server.Close)
			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			engine := NewEngine(
				store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)),
				slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil,
			)
			err = engine.initializeFeed(t.Context(), "100")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			feed, err := store.FeedState("100")
			require.NoError(t, err)
			assert.Equal(t, tt.baseline, feed.UpdateBaseline)
		})
	}
}

func TestCommentOncePollsEligibleTargets(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"page":{"num":1,"size":20,"count":0},"replies":[]}}`)
	}))
	t.Cleanup(server.Close)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.PutUP(model.UP{UID: "42", Enabled: true}))
	_, err = store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}})
	require.NoError(t, err)
	target := model.CommentTarget{UID: "42", CommentType: 11, CommentOID: "oid", PublishedAt: time.Now()}
	require.NoError(t, store.PutCommentTargets("42", []model.CommentTarget{target}))
	engine := NewEngine(
		store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)),
		slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 2), nil, nil,
	)
	engine.authValid.Store(true)
	require.NoError(t, engine.commentOnce(t.Context()))
	targets, err := store.ListAllCommentTargets()
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.True(t, targets[0].BaselineReady)
	assert.False(t, targets[0].LastPollAt.IsZero())
}

func TestMicrosoftLoginLifecycle(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: serviceRoundTripper(func(request *http.Request) (*http.Response, error) {
		body := `{}`
		switch {
		case strings.HasSuffix(request.URL.Path, "/devicecode"):
			body = `{"device_code":"device","user_code":"CODE","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":1}`
		case strings.HasSuffix(request.URL.Path, "/token"):
			body = `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`
		default:
			return serviceResponse(request, http.StatusNotFound, `{}`), nil
		}
		return serviceResponse(request, http.StatusOK, body), nil
	})}
	t.Cleanup(client.CloseIdleConnections)
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	channel, err := store.PutChannel(model.Channel{
		Name: "microsoft", Type: model.ChannelMicrosoft,
		Settings: map[string]string{
			"client_id": "11111111-2222-3333-4444-555555555555", "tenant": "common", "to": "to@example.com",
		},
	})
	require.NoError(t, err)
	engine := NewEngine(
		store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), NewEventBus(), nil,
		WithNotificationHTTPClient(client),
	)
	runCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	engine.microsoftLoginMu.Lock()
	engine.microsoftRunCtx = runCtx
	engine.microsoftLoginMu.Unlock()
	login, err := engine.StartMicrosoftLogin(t.Context(), channel.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", login.Status)
	assert.Equal(t, "CODE", login.UserCode)
	require.Eventually(t, func() bool {
		current, currentErr := engine.MicrosoftLogin(channel.ID)
		return currentErr == nil && current.Status == "success"
	}, 3*time.Second, 10*time.Millisecond)
	updated, err := store.Channel(channel.ID)
	require.NoError(t, err)
	assert.Equal(t, "true", updated.Settings["authorized"])
	assert.Equal(t, "refresh", updated.Settings["refresh_token"])
	logins := engine.MicrosoftLogins()
	require.Len(t, logins, 1)
	assert.Equal(t, channel.ID, logins[0].ChannelID)
	_, err = engine.MicrosoftLogin("missing")
	assert.ErrorIs(t, err, ErrMicrosoftLoginNotFound)
	engine.CancelMicrosoftLogin(channel.ID)
	cancel()
	engine.microsoftLoginWG.Wait()
}

type serviceRoundTripper func(*http.Request) (*http.Response, error)

func (f serviceRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func serviceResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestSetAuthQueuesLifecycleAlerts(t *testing.T) {
	t.Parallel()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.PutChannel(model.Channel{Name: "robot", Type: model.ChannelWeCom, Enabled: true, Settings: map[string]string{"webhook": "https://example.com/hook"}})
	require.NoError(t, err)
	engine := NewEngine(store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), NewEventBus(), nil)
	engine.setAuth(true)
	engine.setAuth(false)
	engine.setAuth(true)
	deliveries, err := store.ListDeliveries(0)
	require.NoError(t, err)
	require.Len(t, deliveries, 2)
	assert.Contains(t, deliveries[0].Dynamic.Summary+deliveries[1].Dynamic.Summary, "登录失效")
	assert.Contains(t, deliveries[0].Dynamic.Summary+deliveries[1].Dynamic.Summary, "登录已恢复")
}

func TestEngineRunLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		storedSession bool
	}{
		{name: "starts and stops without a stored session"},
		{name: "restores a valid stored session", storedSession: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/x/web-interface/nav" {
					_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"isLogin":true,"mid":100,"uname":"account"}}`)
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(server.Close)
			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			if tt.storedSession {
				require.NoError(t, store.SaveSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "session"}}))
			}
			engine := NewEngine(
				store, bilibili.New(server.Client(), "test", bilibili.WithBaseURLs(server.URL, server.URL)),
				slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), nil, nil,
			)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- engine.Run(ctx) }()
			if tt.storedSession {
				require.Eventually(t, engine.authValid.Load, 2*time.Second, 10*time.Millisecond)
				assert.Equal(t, "100", engine.currentAccount().UID)
			}
			cancel()
			require.NoError(t, <-done)
		})
	}
}

func TestHandleCommentPollError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		closed  bool
		risk    bool
		authOff bool
	}{
		{name: "temporary", err: &bilibili.APIError{Kind: bilibili.ErrorTemporary, Message: "temporary"}},
		{name: "authentication", err: &bilibili.APIError{Kind: bilibili.ErrorAuthentication, Message: "expired"}, authOff: true},
		{name: "risk control", err: &bilibili.APIError{Kind: bilibili.ErrorRiskControl, Message: "blocked"}, risk: true},
		{name: "closed", err: &bilibili.APIError{Kind: bilibili.ErrorTemporary, Code: 12002, Message: "closed"}, closed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			require.NoError(t, store.PutCommentTargets("42", []model.CommentTarget{{UID: "42", CommentType: 11, CommentOID: "oid"}}))
			engine := NewEngine(
				store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)),
				NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), NewEventBus(), nil,
			)
			engine.authValid.Store(true)
			target := model.CommentTarget{UID: "42", CommentType: 11, CommentOID: "oid"}
			require.NoError(t, engine.handleCommentPollError(t.Context(), target, tt.err))
			assert.Equal(t, tt.authOff, !engine.authValid.Load())
			assert.Equal(t, tt.risk, engine.riskUntil.Load() > time.Now().Unix())
			targets, listErr := store.ListCommentTargets("42")
			require.NoError(t, listErr)
			require.Len(t, targets, 1)
			assert.Equal(t, tt.closed, targets[0].Closed)
		})
	}
}

func TestEngineErrorAndMediaHelpers(t *testing.T) {
	t.Parallel()
	store, err := state.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"), mustTestVault(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.PutUP(model.UP{UID: "42", Name: "UP", Enabled: true}))
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	}))
	t.Cleanup(imageServer.Close)
	engine := NewEngine(
		store, bilibili.New(nil, "test"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewMetrics(metricnoop.NewMeterProvider()), testSettings(30, 10, 1), NewEventBus(),
		&media.Downloader{DataDir: t.TempDir(), Client: imageServer.Client(), UserAgent: "test", AllowPrivateNetwork: true},
	)

	engine.handleBiliAPIError(&bilibili.APIError{Kind: bilibili.ErrorRiskControl, Message: "blocked"})
	assert.Greater(t, engine.riskUntil.Load(), time.Now().Unix())
	engine.authValid.Store(true)
	engine.handleBiliAPIError(&bilibili.APIError{Kind: bilibili.ErrorAuthentication, Message: "expired"})
	assert.False(t, engine.authValid.Load())
	require.NoError(t, engine.failFeed(t.Context(), []model.UP{{UID: "42"}}, time.Now(), errors.New("feed failed")))
	up, err := store.UP("42")
	require.NoError(t, err)
	assert.Equal(t, 1, up.ConsecutiveFail)

	items := []model.Dynamic{{
		ID: "dynamic", UID: "42", Media: []model.DynamicMedia{
			{Kind: model.DynamicMediaImage, URL: imageServer.URL + "/image.png"},
			{Kind: model.DynamicMediaImage},
		},
	}}
	engine.enrichMedia(t.Context(), items)
	assert.NotEmpty(t, items[0].Media[0].LocalPath)
	assert.Empty(t, items[0].Media[1].LocalPath)
}
