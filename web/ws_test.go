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
)

func TestWebSocketRequiresSessionAndPublishesUpdates(t *testing.T) {
	store := openWebTestStore(t)
	auth, setupCode, err := newAuthenticator(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.initialize(setupCode, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	events := service.NewEventBus()
	server := &Server{
		auth: auth,
		engine: service.NewEngine(
			store,
			bilibili.New(nil, "test"),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			nil,
			model.RuntimeSettings{PollIntervalSec: 30, RequestRate: 2, RequestConcurrency: 4},
			events,
		),
		store:       store,
		events:      events,
		connections: make(map[string]map[*websocket.Conn]struct{}),
	}
	httpServer := httptest.NewTLSServer(server.adminHandler())
	defer httpServer.Close()
	wsURL := "wss" + strings.TrimPrefix(httpServer.URL, "https") + "/api/v1/ws"

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpServer.Client()})
	if connection != nil {
		_ = connection.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dial: err=%v status=%v", err, responseStatus(response))
	}
	_ = response.Body.Close()

	token, _, err := auth.createSession()
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	headers.Set("Cookie", (&http.Cookie{Name: sessionCookie, Value: token}).String())
	headers.Set("Origin", httpServer.URL)
	connection, response, err = websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpServer.Client(), HTTPHeader: headers})
	if err != nil {
		t.Fatalf("authenticated dial: %v (status=%v)", err, responseStatus(response))
	}
	defer connection.CloseNow()

	var initial testWSEnvelope
	if err := wsjson.Read(ctx, connection, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Event != "snapshot" {
		t.Fatalf("first event=%q, want snapshot", initial.Event)
	}
	var snapshot dashboardSnapshot
	if err := json.Unmarshal(initial.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.UPs == nil || snapshot.Channels == nil || snapshot.Deliveries == nil || snapshot.MicrosoftLogins == nil {
		t.Fatalf("snapshot contains null collections: %#v", snapshot)
	}

	request := wsRequest{
		ID:      "create-up",
		Action:  "up.create",
		Payload: json.RawMessage(`{"uid":"42","name":"Test UP","enabled":true}`),
	}
	if err := wsjson.Write(ctx, connection, request); err != nil {
		t.Fatal(err)
	}

	var gotResponse, gotUpdate bool
	for range 4 {
		var envelope testWSEnvelope
		if err := wsjson.Read(ctx, connection, &envelope); err != nil {
			t.Fatal(err)
		}
		switch {
		case envelope.ID == request.ID:
			if !envelope.OK || envelope.Error != nil {
				t.Fatalf("command failed: %#v", envelope.Error)
			}
			var up model.UP
			if err := json.Unmarshal(envelope.Data, &up); err != nil {
				t.Fatal(err)
			}
			if up.UID != "42" || up.Name != "Test UP" || !up.Enabled {
				t.Fatalf("unexpected response UP: %#v", up)
			}
			gotResponse = true
		case envelope.Event == "ups.updated":
			var ups []model.UP
			if err := json.Unmarshal(envelope.Data, &ups); err != nil {
				t.Fatal(err)
			}
			if len(ups) != 1 || ups[0].UID != "42" || envelope.Revision == 0 {
				t.Fatalf("unexpected UP update: revision=%d ups=%#v", envelope.Revision, ups)
			}
			gotUpdate = true
		}
		if gotResponse && gotUpdate {
			break
		}
	}
	if !gotResponse || !gotUpdate {
		t.Fatalf("got response=%v update=%v", gotResponse, gotUpdate)
	}
}

func TestDeliveryViewsExcludeRichPayloadAndStayBounded(t *testing.T) {
	deliveries := make([]model.Delivery, 100)
	for i := range deliveries {
		deliveries[i] = model.Delivery{
			ID: "delivery",
			Dynamic: model.Dynamic{
				ID: "dynamic", UID: "42", UPName: "up", Type: "DYNAMIC_TYPE_DRAW",
				PublishedAt: time.Now().UTC(), Summary: strings.Repeat("正文", 5000), URL: "https://t.bilibili.com/1",
				Description: strings.Repeat("不应进入管理台", 5000),
				Media:       []model.DynamicMedia{{Kind: model.DynamicMediaImage, URL: "https://i0.hdslb.com/image.jpg"}},
				Original:    &model.Dynamic{ID: "original", Summary: strings.Repeat("原文", 5000)},
			},
			State: model.DeliveryPending,
		}
	}
	views := deliveryViews(deliveries)
	raw, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 1<<20 {
		t.Fatalf("delivery views size = %d", len(raw))
	}
	if strings.Contains(string(raw), "不应进入管理台") || len([]rune(views[0].Dynamic.Summary)) > 241 {
		t.Fatalf("delivery view contains rich payload: %s", raw[:min(len(raw), 1000)])
	}
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
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func responseStatus(response *http.Response) any {
	if response == nil {
		return nil
	}
	return response.StatusCode
}
