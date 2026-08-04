package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
)

func TestWeComSender(t *testing.T) {
	var got map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	sender, err := NewSender(model.Channel{
		Name: "wecom", Type: model.ChannelWeCom,
		Settings: map[string]string{"webhook": server.URL},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), Message{Subject: "s", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if got["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v", got["msgtype"])
	}
}

func TestMicrosoftSenderRefreshesTokenAndSendsGraphMail(t *testing.T) {
	var gotAuthorization string
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("refresh_token") != "old-refresh" {
				t.Errorf("refresh_token = %q", r.Form.Get("refresh_token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
		case "/send":
			gotAuthorization = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := map[string]string{
		"client_id": "11111111-2222-3333-4444-555555555555", "tenant": "common",
		"to": "one@example.com,Two <two@example.com>", "access_token": "old-access",
		"refresh_token": "old-refresh", "token_type": "Bearer",
		"token_expiry": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
	}
	var updated map[string]string
	sender := newMicrosoftSender(settings, server.Client(), func(values map[string]string) error {
		updated = values
		return nil
	}, microsoftEndpoints{tokenURL: server.URL + "/token", graphSendURL: server.URL + "/send"})
	if err := sender.Send(t.Context(), Message{Subject: "subject", HTML: "<p>body</p>"}); err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "Bearer new-access" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if updated["refresh_token"] != "new-refresh" || updated["authorized"] != "true" {
		t.Fatalf("updated settings = %#v", updated)
	}
	message, ok := gotPayload["message"].(map[string]any)
	if !ok || message["subject"] != "subject" {
		t.Fatalf("payload = %#v", gotPayload)
	}
	recipients, ok := message["toRecipients"].([]any)
	if !ok || len(recipients) != 2 {
		t.Fatalf("recipients = %#v", message["toRecipients"])
	}
}

func TestStartMicrosoftDeviceAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.Form.Get("scope"), "Mail.Send") {
			t.Errorf("scope = %q", r.Form.Get("scope"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":5}`))
	}))
	defer server.Close()

	auth, err := startMicrosoftDeviceAuth(t.Context(), map[string]string{
		"client_id": "11111111-2222-3333-4444-555555555555",
	}, server.Client(), microsoftEndpoints{deviceAuthURL: server.URL + "/device", tokenURL: server.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}
	if auth.UserCode != "ABCD-EFGH" || auth.VerificationURI != "https://microsoft.com/devicelogin" {
		t.Fatalf("authorization = %#v", auth)
	}
}
