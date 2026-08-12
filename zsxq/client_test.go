package zsxq

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientSendsWebSessionRequest(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 12, 1, 2, 3, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/groups", r.URL.Path)
		cookie, err := r.Cookie(AccessTokenKey)
		require.NoError(t, err)
		assert.Equal(t, "secret-token", cookie.Value)
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Equal(t, "2.37.0", r.Header.Get("X-Version"))
		assert.Equal(t, fmt.Sprint(now.Unix()), r.Header.Get("X-Timestamp"))
		assert.Equal(t, "application/json, text/plain, */*", r.Header.Get("Accept"))
		assert.Equal(t, "zh-CN,zh;q=0.9,en;q=0.8", r.Header.Get("Accept-Language"))
		assert.Equal(t, webUserAgent, r.Header.Get("User-Agent"))
		assert.Empty(t, r.Header.Get("X-Signature"))
		writeEnvelope(t, w, map[string]any{"groups": []any{}})
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), WithBaseURL(server.URL), withNow(func() time.Time { return now }))
	require.NoError(t, err)
	groups, err := client.Groups(t.Context(), "secret-token")
	require.NoError(t, err)
	assert.Empty(t, groups)
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
				assert.Equal(t, "/v2/groups", r.URL.Path)
				cookie, err := r.Cookie(AccessTokenKey)
				require.NoError(t, err)
				assert.Equal(t, "secret-token", cookie.Value)
				writeEnvelope(t, w, map[string]any{"groups": tt.groups})
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), WithBaseURL(server.URL))
			require.NoError(t, err)
			groups, err := client.Groups(t.Context(), "secret-token")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, groups)
		})
	}
}

func TestAccountManagerGroupsInvalidatesRejectedSession(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), WithBaseURL(server.URL))
	require.NoError(t, err)
	store := &accountStoreStub{account: model.PlatformAccount{Platform: model.PlatformZSXQ, Status: model.AccountConnected, Session: map[string]string{AccessTokenKey: "rejected"}}}
	manager, err := NewAccountManager(client, store)
	require.NoError(t, err)
	_, err = manager.Groups(t.Context())
	require.ErrorIs(t, err, ErrAuthentication)
	assert.Equal(t, model.AccountInvalid, store.account.Status)
	assert.Empty(t, store.account.Session)
}

func TestAccountManagerImportsSessionThroughGroupDiscovery(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/groups", r.URL.Path)
		writeEnvelope(t, w, map[string]any{"groups": []any{}})
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), WithBaseURL(server.URL))
	require.NoError(t, err)
	store := &accountStoreStub{}
	manager, err := NewAccountManager(client, store)
	require.NoError(t, err)

	account, err := manager.ImportCookie(t.Context(), "zsxq_access_token=session")
	require.NoError(t, err)
	assert.Empty(t, account.ExternalID)
	assert.Equal(t, "知识星球网页会话", account.DisplayName)
	assert.Equal(t, model.AccountConnected, account.Status)
	assert.Nil(t, account.Session)
	assert.Equal(t, "session", store.account.Session[AccessTokenKey])
}

func TestClientClassifiesFailuresWithoutLeakingBodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "authentication", status: http.StatusUnauthorized, body: "secret-token", want: ErrAuthentication},
		{name: "permission", status: http.StatusForbidden, body: "private-topic", want: ErrPermission},
		{name: "rate limit", status: http.StatusTooManyRequests, body: "private-topic", want: ErrRateLimited},
		{name: "not found", status: http.StatusNotFound, body: "signed-url", want: ErrRemoteNotFound},
		{name: "invalid token code", status: http.StatusOK, body: `{"succeeded":false,"code":10001}`, want: ErrAuthentication},
		{name: "signature code", status: http.StatusOK, body: `{"succeeded":false,"code":10003}`, want: ErrUpstream},
		{name: "limit code", status: http.StatusOK, body: `{"succeeded":false,"code":40001}`, want: ErrRateLimited},
		{name: "schema drift", status: http.StatusOK, body: `{`, want: ErrSchemaDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), WithBaseURL(server.URL))
			require.NoError(t, err)
			_, err = client.Groups(t.Context(), "token")
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
			assert.NotContains(t, err.Error(), tt.body)
		})
	}
}

func TestClientClassifiesLoginRedirect(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "https://wx.zsxq.com/dweb2/login", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), WithBaseURL(server.URL))
	require.NoError(t, err)

	_, err = client.Groups(t.Context(), "expired")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthentication)
}

func TestDecodeTopicDetailShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
		wantID  string
		wantErr error
	}{
		{name: "wrapped info response", payload: `{"topic":{"topic_id":1,"type":"talk"}}`, wantID: "1"},
		{name: "direct info response", payload: `{"topic_id":2,"type":"talk"}`, wantID: "2"},
		{name: "missing topic id", payload: `{"type":"talk"}`, wantErr: ErrSchemaDrift},
		{name: "invalid JSON", payload: `{`, wantErr: ErrSchemaDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			topic, err := decodeTopicDetail(json.RawMessage(tt.payload))
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, topic.TopicID.String())
		})
	}
}

func TestClientTopicParsesPreviewCommentsAndResolvesFileOnDemand(t *testing.T) {
	t.Parallel()
	var paths []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		cookie, err := r.Cookie(AccessTokenKey)
		require.NoError(t, err)
		assert.Equal(t, "token", cookie.Value)
		switch r.URL.Path {
		case "/v2/topics/22255155254188541/info":
			writeEnvelope(t, w, map[string]any{"topic": map[string]any{
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
			writeEnvelope(t, w, map[string]any{"download_url": server.URL + "/signed/file"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), WithBaseURL(server.URL))
	require.NoError(t, err)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "28882581855851"), Platform: model.PlatformZSXQ,
		Type: model.SourceZSXQPlanet, ExternalID: "28882581855851", OwnerID: "548818848124544"}

	snapshot, err := client.Topic(t.Context(), "token", source, "22255155254188541")
	require.NoError(t, err)
	require.Len(t, snapshot.Attachments, 1)
	require.Len(t, snapshot.ShownComments, 1)
	assert.Equal(t, []string{"/v2/topics/22255155254188541/info"}, paths)
	require.NoError(t, client.populateFileDownloadURLs(t.Context(), "token", snapshot.Attachments))
	assert.Equal(t, []string{"/v2/topics/22255155254188541/info", "/v2/files/814511428244812/download_url"}, paths)
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), WithBaseURL(server.URL))
			require.NoError(t, err)
			attachments := []model.Attachment{{ExternalID: "1", Type: model.AttachmentFile}}

			err = client.populateFileDownloadURLs(t.Context(), "token", attachments)
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

func TestParseAccessToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw, want string
		wantErr         bool
	}{
		{name: "single", raw: "zsxq_access_token=abc", want: "abc"},
		{name: "multiple cookies", raw: "foo=bar; zsxq_access_token=a-b.c; theme=dark", want: "a-b.c"},
		{name: "missing", raw: "foo=bar", wantErr: true},
		{name: "empty", raw: "zsxq_access_token=", wantErr: true},
		{name: "duplicate", raw: "zsxq_access_token=a; zsxq_access_token=b", wantErr: true},
		{name: "control", raw: "zsxq_access_token=a\r\nInjected: yes", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseAccessToken(tt.raw)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidCookie)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func FuzzParseAccessToken(f *testing.F) {
	f.Add("zsxq_access_token=abc")
	f.Add("foo=bar")
	f.Fuzz(func(t *testing.T, raw string) { _, _ = ParseAccessToken(raw) })
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

func writeEnvelope(t *testing.T, writer http.ResponseWriter, data any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"succeeded": true, "code": 0, "error": "", "info": "", "resp_data": data}))
}

func Example_publicError() {
	fmt.Println(publicError(errors.Join(ErrAuthentication, errors.New("token=secret"))))
	// Output: authentication expired
}

func TestUnsupportedClientBusinessCode(t *testing.T) {
	t.Parallel()
	err := decodeEnvelope([]byte(`{"succeeded":false,"code":1059,"error":"must-not-leak","info":"must-not-leak"}`), nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedClient)
	assert.NotContains(t, publicError(err), "1059")
	assert.Contains(t, publicError(err), "重新导入 Session")
}
