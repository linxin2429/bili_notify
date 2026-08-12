package zsxq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientCallsOfficialMCPWithAPIKey(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, method, path, _ := readMCPCall(t, r)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/", r.URL.Path)
		assert.Equal(t, "secret-key", r.Header.Get("X-Api-Key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("Cookie"))
		assert.Empty(t, r.Header.Get("Origin"))
		assert.Equal(t, http.MethodGet, method)
		assert.Equal(t, "/v3/users/self", path)
		writeMCPEnvelope(t, w, id, http.StatusOK, map[string]any{"user": map[string]any{"uid": 42, "name": "Member"}})
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), "test", WithBaseURL(server.URL))
	require.NoError(t, err)
	user, err := client.Me(t.Context(), "secret-key")
	require.NoError(t, err)
	assert.Equal(t, User{ID: "42", Name: "Member"}, user)
}

func TestClientGroups(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		groups  []map[string]any
		want    []Group
		wantErr error
	}{
		{name: "visible groups preserve numeric identifiers", groups: []map[string]any{{"group_id": json.Number("28882581855851"), "name": "研究星球", "owner": map[string]any{"user_id": json.Number("548818848124544"), "name": "星主"}}}, want: []Group{{ID: "28882581855851", Name: "研究星球", OwnerID: "548818848124544", OwnerName: "星主"}}},
		{name: "owner is optional", groups: []map[string]any{{"group_id": 9, "name": "普通星球"}}, want: []Group{{ID: "9", Name: "普通星球"}}},
		{name: "owner uid variant", groups: []map[string]any{{"group_id": 10, "name": "UID 星球", "owner": map[string]any{"uid": 7, "name": "星主"}}}, want: []Group{{ID: "10", Name: "UID 星球", OwnerID: "7", OwnerName: "星主"}}},
		{name: "missing group name", groups: []map[string]any{{"group_id": 9}}, wantErr: ErrSchemaDrift},
		{name: "invalid group identifier", groups: []map[string]any{{"group_id": "not-a-number", "name": "错误星球"}}, wantErr: ErrSchemaDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, _, path, _ := readMCPCall(t, r)
				assert.Equal(t, "/v2/groups", path)
				assert.Equal(t, "secret-key", r.Header.Get("X-Api-Key"))
				writeMCPEnvelope(t, w, id, http.StatusOK, map[string]any{"groups": tt.groups})
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL))
			require.NoError(t, err)
			groups, err := client.Groups(t.Context(), "secret-key")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, groups)
		})
	}
}

func TestAccountManagerGroupsInvalidatesRejectedCredential(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), "test", WithBaseURL(server.URL))
	require.NoError(t, err)
	store := &accountStoreStub{account: model.PlatformAccount{Platform: model.PlatformZSXQ, Status: model.AccountConnected, Session: map[string]string{APIKeyCredential: "rejected"}}}
	manager, err := NewAccountManager(client, store)
	require.NoError(t, err)
	_, err = manager.Groups(t.Context())
	require.ErrorIs(t, err, ErrAuthentication)
	assert.Equal(t, model.AccountInvalid, store.account.Status)
	assert.Empty(t, store.account.Session)
}

func TestClientClassifiesFailuresWithoutLeakingBodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "authentication", status: http.StatusUnauthorized, body: "secret-key", want: ErrAuthentication},
		{name: "permission", status: http.StatusForbidden, body: "private-topic", want: ErrPermission},
		{name: "rate limit", status: http.StatusTooManyRequests, body: "private-topic", want: ErrRateLimited},
		{name: "not found", status: http.StatusNotFound, body: "signed-url", want: ErrRemoteNotFound},
		{name: "invalid credential code", status: http.StatusOK, body: `{"succeeded":false,"code":10001}`, want: ErrAuthentication},
		{name: "signature code", status: http.StatusOK, body: `{"succeeded":false,"code":10003}`, want: ErrUpstream},
		{name: "limit code", status: http.StatusOK, body: `{"succeeded":false,"code":40001}`, want: ErrRateLimited},
		{name: "schema drift", status: http.StatusOK, body: `{`, want: ErrSchemaDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status == http.StatusOK {
					id, _, _, _ := readMCPCall(t, r)
					writeMCPRaw(t, w, id, http.StatusOK, tt.body)
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL))
			require.NoError(t, err)
			_, err = client.Me(t.Context(), "key")
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
			assert.NotContains(t, err.Error(), tt.body)
		})
	}
}

func TestDecodeMCPMessageRejectsInvalidSSE(t *testing.T) {
	t.Parallel()
	valid := `{"jsonrpc":"2.0","id":7,"result":{"content":[]}}`
	tests := []struct {
		name string
		body string
	}{
		{name: "missing message", body: "event: ping\ndata: {}\n\n"},
		{name: "wrong id", body: "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":8,\"result\":{\"content\":[]}}\n\n"},
		{name: "duplicate id", body: "event: message\ndata: " + valid + "\n\nevent: message\ndata: " + valid + "\n\n"},
		{name: "malformed JSON", body: "event: message\ndata: {\n\n"},
		{name: "trailing JSON", body: "event: message\ndata: " + valid + " {}\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeMCPMessage([]byte(tt.body), "text/event-stream", 7)
			require.ErrorIs(t, err, ErrSchemaDrift)
		})
	}
}

func TestDecodeMCPMessageAcceptsSSEDataFramesAndJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "SSE data frames", contentType: "text/event-stream; charset=utf-8", body: "event: message\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"id\":7,\"result\":{\"content\":[]}}\n\n"},
		{name: "JSON response", contentType: "application/json", body: `{"jsonrpc":"2.0","id":7,"result":{"content":[]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			message, err := decodeMCPMessage([]byte(tt.body), tt.contentType, 7)
			require.NoError(t, err)
			assert.Equal(t, "2.0", message.JSONRPC)
		})
	}
}

func TestClientRejectsOversizedResponseAndRedirectWithoutCredentialLeak(t *testing.T) {
	t.Parallel()
	var redirectedKey string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedKey = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{name: "oversized", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, maxResponseSize+1)) }, want: ErrSchemaDrift},
		{name: "redirect", handler: func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }, want: ErrUpstream},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tt.handler)
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL))
			require.NoError(t, err)
			_, err = client.Me(t.Context(), "must-not-leak")
			require.ErrorIs(t, err, tt.want)
		})
	}
	assert.Empty(t, redirectedKey)
}

func TestClientPreservesContextCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), "test", WithBaseURL(server.URL))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		_, callErr := client.Me(ctx, "key")
		done <- callErr
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(release)
}

func TestClientTopicParsesPreviewCommentsAndResolvesFileOnDemand(t *testing.T) {
	t.Parallel()
	var paths []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _, path, _ := readMCPCall(t, r)
		paths = append(paths, path)
		assert.Equal(t, "key", r.Header.Get("X-Api-Key"))
		switch path {
		case "/v2/topics/22255155254188541":
			writeMCPEnvelope(t, w, id, http.StatusOK, map[string]any{"topic": map[string]any{
				"topic_id": 22255155254188541, "type": "talk", "title": "SemiAnalysis的NV...",
				"create_time": "2026-08-12T14:55:00.479+0800", "comments_count": 1,
				"talk": map[string]any{
					"owner": map[string]any{"user_id": 548818848124544, "name": "小小"},
					"text":  `正文 <e type="hashtag" hid="1" title="%23SemiAnalysis%23" />`,
					"files": []map[string]any{{"file_id": 814511428244812, "name": "资料.pdf", "size": 6483608, "duration": 0}},
				},
				"show_comments": []map[string]any{{
					"comment_id": 4842482254125488, "create_time": "2026-08-12T21:53:56.094+0800",
					"owner": map[string]any{"user_id": 218584241484151, "name": "李明阳"}, "text": "test",
				}},
			}})
		case "/v2/files/814511428244812/download_url":
			writeMCPEnvelope(t, w, id, http.StatusOK, map[string]any{"download_url": server.URL + "/signed/file"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), "test", WithBaseURL(server.URL))
	require.NoError(t, err)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "28882581855851"), Platform: model.PlatformZSXQ,
		Type: model.SourceZSXQPlanet, ExternalID: "28882581855851", OwnerID: "548818848124544"}

	snapshot, err := client.Topic(t.Context(), "key", source, "22255155254188541")
	require.NoError(t, err)
	require.Len(t, snapshot.Attachments, 1)
	require.Len(t, snapshot.ShownComments, 1)
	assert.Equal(t, []string{"/v2/topics/22255155254188541"}, paths)
	require.NoError(t, client.populateFileDownloadURLs(t.Context(), "key", snapshot.Attachments))
	assert.Equal(t, []string{"/v2/topics/22255155254188541", "/v2/files/814511428244812/download_url"}, paths)
	assert.Equal(t, "正文 #SemiAnalysis#", snapshot.Content.Text)
	assert.Empty(t, snapshot.Content.Title)
	assert.Equal(t, server.URL+"/signed/file", snapshot.Attachments[0].RemoteURL)
	assert.True(t, snapshot.CommentsComplete)
	assert.Equal(t, "test", snapshot.ShownComments[0].Message)
}

func TestClientFileDownloadResolution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		status       int
		body         string
		wantErr      error
		wantLocalize string
	}{
		{name: "permission keeps metadata", status: http.StatusForbidden, wantLocalize: "attachment download unavailable"},
		{name: "not found keeps metadata", status: http.StatusNotFound, wantLocalize: "attachment download unavailable"},
		{name: "upstream failure is retried with topic", status: http.StatusInternalServerError, wantErr: ErrUpstream},
		{name: "invalid URL is schema drift", status: http.StatusOK, body: `{"succeeded":true,"resp_data":{"download_url":"relative"}}`, wantErr: ErrSchemaDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status != http.StatusOK {
					id, _, _, _ := readMCPCall(t, r)
					writeMCPRaw(t, w, id, tt.status, `{}`)
					return
				}
				id, _, _, _ := readMCPCall(t, r)
				writeMCPRaw(t, w, id, http.StatusOK, tt.body)
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL))
			require.NoError(t, err)
			attachments := []model.Attachment{{ExternalID: "1", Type: model.AttachmentFile}}

			err = client.populateFileDownloadURLs(t.Context(), "key", attachments)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantLocalize, attachments[0].LocalizeError)
		})
	}
}

func TestNormalizeAPIKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw, want string
		wantErr         bool
	}{
		{name: "trimmed opaque key", raw: "  a-b.c  ", want: "a-b.c"},
		{name: "empty", raw: "  ", wantErr: true},
		{name: "control", raw: "a\r\nInjected: yes", wantErr: true},
		{name: "unicode control", raw: "a\u0085b", wantErr: true},
		{name: "too large", raw: string(make([]byte, (8<<10)+1)), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeAPIKey(tt.raw)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidAPIKey)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func FuzzNormalizeAPIKey(f *testing.F) {
	f.Add("opaque-key")
	f.Add("\r\n")
	f.Fuzz(func(t *testing.T, raw string) { _, _ = NormalizeAPIKey(raw) })
}

type accountStoreStub struct {
	account model.PlatformAccount
}

func (store *accountStoreStub) PlatformAccount(model.Platform) (model.PlatformAccount, error) {
	if store.account.Platform == "" {
		return model.PlatformAccount{}, errors.New("not found")
	}
	return store.account, nil
}

func (store *accountStoreStub) PutPlatformAccount(account model.PlatformAccount) error {
	store.account = account
	return nil
}

func writeMCPEnvelope(t *testing.T, writer http.ResponseWriter, id json.RawMessage, status int, data any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"succeeded": true, "code": 0, "error": "", "info": "", "resp_data": data})
	require.NoError(t, err)
	writeMCPRaw(t, writer, id, status, string(body))
}

func Example_publicError() {
	fmt.Println(publicError(errors.Join(ErrAuthentication, errors.New("api-key=secret"))))
	// Output: API key update required
}

func TestUnknownBusinessCodeIsUpstreamFailure(t *testing.T) {
	t.Parallel()
	err := decodeEnvelope([]byte(`{"succeeded":false,"code":1059,"error":"must-not-leak","info":"must-not-leak"}`), nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUpstream)
	assert.NotContains(t, publicError(err), "1059")
}

func readMCPCall(t *testing.T, request *http.Request) (json.RawMessage, string, string, map[string]any) {
	t.Helper()
	var call struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	require.NoError(t, json.NewDecoder(request.Body).Decode(&call))
	assert.Equal(t, "2.0", call.JSONRPC)
	assert.Equal(t, "tools/call", call.Method)
	assert.Equal(t, "call_zsxq_api", call.Params.Name)
	method, _ := call.Params.Arguments["method"].(string)
	path, _ := call.Params.Arguments["path"].(string)
	return call.ID, method, path, call.Params.Arguments
}

func writeMCPRaw(t *testing.T, writer http.ResponseWriter, id json.RawMessage, status int, body string) {
	t.Helper()
	success := status >= 200 && status < 300
	var proxyBody any = json.RawMessage(body)
	if !json.Valid([]byte(body)) {
		proxyBody = body
	}
	text, err := json.Marshal(map[string]any{"success": success, "status_code": status, "body": proxyBody})
	require.NoError(t, err)
	message := map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}, "isError": !success}}
	encoded, err := json.Marshal(message)
	require.NoError(t, err)
	writer.Header().Set("Content-Type", "text/event-stream")
	_, err = fmt.Fprintf(writer, "event: message\ndata: %s\n\n", encoded)
	require.NoError(t, err)
}
