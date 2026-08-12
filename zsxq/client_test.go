package zsxq

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
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

func TestClientSignsExplicitTokenRequest(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 12, 1, 2, 3, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v3/users/self", r.URL.Path)
		assert.Equal(t, "secret-token", r.Header.Get("Authorization"))
		assert.Equal(t, "2.83.0", r.Header.Get("X-Version"))
		assert.Equal(t, "request-id", r.Header.Get("X-Request-Id"))
		assert.Equal(t, "device-id", r.Header.Get("X-Aduid"))
		plain := fmt.Sprintf("%d\nGET\n/v3/users/self", now.Unix())
		digest := hmac.New(sha1.New, []byte(signingSecret))
		_, _ = digest.Write([]byte(plain))
		assert.Equal(t, hex.EncodeToString(digest.Sum(nil)), r.Header.Get("X-Signature"))
		writeEnvelope(t, w, map[string]any{"user": map[string]any{"uid": 42, "name": "Member"}})
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), "test", WithBaseURL(server.URL), withProtocolValues(func() time.Time { return now }, func() string { return "request-id" }, "device-id"))
	require.NoError(t, err)
	user, err := client.Me(t.Context(), "secret-token")
	require.NoError(t, err)
	assert.Equal(t, User{ID: "42", Name: "Member"}, user)
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
			client, err := New(server.Client(), "test", WithBaseURL(server.URL))
			require.NoError(t, err)
			_, err = client.Me(t.Context(), "token")
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
			assert.NotContains(t, err.Error(), tt.body)
		})
	}
}

func TestClientTopicParsesPreviewCommentsAndResolvesFileOnDemand(t *testing.T) {
	t.Parallel()
	var paths []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		assert.Equal(t, "token", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v2/topics/22255155254188541":
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
	client, err := New(server.Client(), "test", WithBaseURL(server.URL))
	require.NoError(t, err)
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "28882581855851"), Platform: model.PlatformZSXQ,
		Type: model.SourceZSXQPlanet, ExternalID: "28882581855851", OwnerID: "548818848124544"}

	snapshot, err := client.Topic(t.Context(), "token", source, "22255155254188541")
	require.NoError(t, err)
	require.Len(t, snapshot.Attachments, 1)
	require.Len(t, snapshot.ShownComments, 1)
	assert.Equal(t, []string{"/v2/topics/22255155254188541"}, paths)
	require.NoError(t, client.populateFileDownloadURLs(t.Context(), "token", snapshot.Attachments))
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL))
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
	sources []model.Source
}

func (store *accountStoreStub) PlatformAccount(model.Platform) (model.PlatformAccount, error) {
	return store.account, nil
}
func (store *accountStoreStub) ReplaceZSXQPlatformAccount(account model.PlatformAccount) error {
	store.account = account
	return nil
}
func (store *accountStoreStub) MergeVisibleSources(_ model.Platform, sources []model.Source) error {
	store.sources = sources
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
