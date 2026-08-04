package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebSocketRequiresSessionAndPublishesUpdates(t *testing.T) {
	t.Parallel()
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	require.NoError(t, err)
	require.NoError(t, auth.initialize(setupCode, "correct horse battery staple"))
	events := service.NewEventBus()
	server := &Server{
		auth: auth,
		engine: service.NewEngine(
			store,
			bilibili.New(nil, "test"),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			nil,
			model.RuntimeSettings{
				PollIntervalSec: 30, RequestRate: 2, RequestConcurrency: 4,
				CommentEnabled: true, CommentTrackN: 10, CommentRootPages: 2,
				CommentReplyPages: 5, CommentBatchIntervalSec: 120,
			},
			events,
		),
		store:       store,
		events:      events,
		connections: make(map[string]map[*websocket.Conn]struct{}),
	}
	httpServer := httptest.NewTLSServer(server.adminHandler())
	t.Cleanup(httpServer.Close)
	wsURL := "wss" + strings.TrimPrefix(httpServer.URL, "https") + "/api/v1/ws"

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	connection, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpServer.Client()})
	if connection != nil {
		_ = connection.CloseNow()
	}
	require.Error(t, err)
	require.NotNil(t, response)
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	_ = response.Body.Close()

	token, _, err := auth.createSession()
	require.NoError(t, err)
	headers := http.Header{}
	headers.Set("Cookie", (&http.Cookie{Name: sessionCookie, Value: token}).String())
	headers.Set("Origin", httpServer.URL)
	connection, response, err = websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpServer.Client(), HTTPHeader: headers})
	require.NoError(t, err, "authenticated dial status=%v", responseStatus(response))
	t.Cleanup(func() { _ = connection.CloseNow() })

	var initial testWSEnvelope
	require.NoError(t, wsjson.Read(ctx, connection, &initial))
	assert.Equal(t, "snapshot", initial.Event)
	var snapshot dashboardSnapshot
	require.NoError(t, json.Unmarshal(initial.Data, &snapshot))
	assert.NotNil(t, snapshot.UPs)
	assert.NotNil(t, snapshot.Channels)
	assert.NotNil(t, snapshot.Deliveries)
	assert.NotNil(t, snapshot.MicrosoftLogins)

	request := wsRequest{
		ID:      "create-up",
		Action:  "up.create",
		Payload: json.RawMessage(`{"uid":"42","name":"Test UP","enabled":true}`),
	}
	require.NoError(t, wsjson.Write(ctx, connection, request))

	var gotResponse, gotUpdate bool
	for range 4 {
		var envelope testWSEnvelope
		require.NoError(t, wsjson.Read(ctx, connection, &envelope))
		switch {
		case envelope.ID == request.ID:
			require.True(t, envelope.OK)
			assert.Nil(t, envelope.Error)
			var up model.UP
			require.NoError(t, json.Unmarshal(envelope.Data, &up))
			assert.Equal(t, "42", up.UID)
			assert.Equal(t, "Test UP", up.Name)
			assert.True(t, up.Enabled)
			gotResponse = true
		case envelope.Event == "ups.updated":
			var ups []model.UP
			require.NoError(t, json.Unmarshal(envelope.Data, &ups))
			require.Len(t, ups, 1)
			assert.Equal(t, "42", ups[0].UID)
			assert.NotZero(t, envelope.Revision)
			gotUpdate = true
		}
		if gotResponse && gotUpdate {
			break
		}
	}
	assert.True(t, gotResponse, "missing command response")
	assert.True(t, gotUpdate, "missing ups.updated event")
}

func TestDeliveryViewsExcludeRichPayloadAndStayBounded(t *testing.T) {
	t.Parallel()
	deliveries := make([]model.Delivery, 100)
	for i := range deliveries {
		deliveries[i] = model.Delivery{
			ID: "delivery",
			Dynamic: model.Dynamic{
				ID: "dynamic", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_DRAW",
				PublishedAt: time.Now(), Summary: strings.Repeat("正文", 5000), URL: "https://t.bilibili.com/1",
				Description: strings.Repeat("不应进入管理台", 5000),
				Media:       []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://i0.hdslb.com/image.jpg"}},
				Original:    &model.Dynamic{ID: "original", Summary: strings.Repeat("原文", 5000)},
			},
			State: model.DeliveryPending,
		}
	}
	views := deliveryViews(deliveries)
	raw, err := json.Marshal(views)
	require.NoError(t, err)
	assert.Less(t, len(raw), 1<<20)
	assert.NotContains(t, string(raw), "不应进入管理台")
	assert.LessOrEqual(t, len([]rune(views[0].Dynamic.Summary)), 241)
}

type testWSEnvelope struct {
	ID       string          `json:"id"`
	OK       bool            `json:"ok"`
	Event    string          `json:"event"`
	Revision uint64          `json:"revision"`
	Data     json.RawMessage `json:"data"`
	Error    *wsAPIError     `json:"error"`
}

func openWebTestStore(t *testing.T) *state.Store {
	t.Helper()
	v, err := vault.New(make([]byte, 32))
	require.NoError(t, err)
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"), v)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func responseStatus(response *http.Response) any {
	if response == nil {
		return nil
	}
	return response.StatusCode
}
