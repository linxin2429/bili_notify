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
