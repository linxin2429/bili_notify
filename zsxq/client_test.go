package zsxq

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientClassifiesUpstreamFailuresWithoutLeakingBodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "authentication", status: http.StatusUnauthorized, body: `secret-cookie`, want: ErrAuthentication},
		{name: "rate limit", status: http.StatusTooManyRequests, body: `private-topic`, want: ErrRateLimited},
		{name: "deleted", status: http.StatusNotFound, body: `signed-url`, want: ErrRemoteNotFound},
		{name: "schema drift", status: http.StatusOK, body: `{`, want: ErrSchemaDrift},
		{name: "legacy succeed false string", status: http.StatusOK, body: `{"code":1059,"succeed":"false"}`, want: ErrAuthentication},
		{name: "invalid SMS code", status: http.StatusOK, body: `{"succeeded":false,"code":10022,"resp_data":{}}`, want: ErrInvalidCode},
		{name: "consumed SMS code", status: http.StatusOK, body: `{"succeeded":false,"code":90012,"resp_data":{}}`, want: ErrInvalidCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL+"/v2"))
			require.NoError(t, err)
			_, err = client.Me(t.Context())
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
			assert.NotContains(t, err.Error(), tt.body)
		})
	}
}

func TestEncryptedLoginAcceptsPlainJSONBusinessFailures(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v3/access_tokens", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"succeeded":false,"code":10022,"resp_data":{}}`))
	}))
	t.Cleanup(server.Close)
	client, err := New(server.Client(), "test", WithBaseURL(server.URL+"/v2"))
	require.NoError(t, err)
	_, err = client.Login(t.Context(), "+86", "13800138000", "123456")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCode)
}

func TestLoginCapturesCookieAndSynchronizesVisiblePlanets(t *testing.T) {
	t.Parallel()
	var requestsMu sync.Mutex
	var requests []string
	var client *Client
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests = append(requests, r.URL.Path)
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		timestamp, requestID := r.Header.Get("X-Timestamp"), r.Header.Get("X-Request-Id")
		signed := sha256.Sum256([]byte("http://" + r.Host + r.URL.RequestURI() + " " + timestamp + " " + requestID))
		assert.Equal(t, hex.EncodeToString(signed[:]), r.Header.Get("X-Signature"))
		assert.NotEmpty(t, r.Header.Get("X-Aduid"))
		switch r.URL.Path {
		case "/v3/verify_codes":
			writeEnvelope(t, w, map[string]any{})
		case "/v3/access_tokens":
			require.NotEmpty(t, r.Header.Get("X-Key"))
			require.NotEmpty(t, r.Header.Get("X-IV"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			plain, err := client.login.decrypt(string(body))
			require.NoError(t, err)
			assert.JSONEq(t, `{"req_data":{"client":"DWeb","phone":{"country_code":"86","phone_number":"13800138000","verify_code":"123456"}}}`, string(plain))
			http.SetCookie(w, &http.Cookie{Name: "zsxq_access_token", Value: "session-secret", Path: "/"})
			envelope, err := json.Marshal(map[string]any{"succeeded": true, "code": 0, "resp_data": map[string]any{}})
			require.NoError(t, err)
			encrypted, err := client.login.encrypt(envelope)
			require.NoError(t, err)
			_, _ = io.WriteString(w, encrypted)
		case "/v3/users/self":
			writeEnvelope(t, w, map[string]any{"user": map[string]any{"user_id": 7, "name": "Member"}})
		case "/v2/groups":
			writeEnvelope(t, w, map[string]any{"groups": []any{map[string]any{"group_id": 9, "name": "Planet", "owner": map[string]any{"user_id": 8, "name": "Owner"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	var err error
	client, err = New(server.Client(), "test", WithBaseURL(server.URL+"/v2"))
	require.NoError(t, err)
	store := &loginStoreStub{}
	manager, err := NewLoginManager(client, store)
	require.NoError(t, err)
	now := time.Date(2026, time.August, 10, 1, 0, 0, 0, time.UTC)
	manager.SetClockForTest(func() time.Time { return now })
	transaction, err := manager.SendCode(t.Context(), SMSCodeRequest{CountryCode: "+86", Phone: "13800138000", CaptchaVerifyParam: "captcha", AgreementAccepted: true})
	require.NoError(t, err)
	assert.Equal(t, "+86 138****8000", transaction.MaskedPhone)
	_, err = manager.SendCode(t.Context(), SMSCodeRequest{CountryCode: "+86", Phone: "13800138000", CaptchaVerifyParam: "captcha", AgreementAccepted: true})
	assert.ErrorIs(t, err, ErrSMSCooldown)

	account, err := manager.SubmitCode(t.Context(), transaction.ID, "123456")
	require.NoError(t, err)
	assert.Equal(t, model.AccountConnected, account.Status)
	assert.Nil(t, account.Session)
	require.Len(t, store.accounts, 1)
	assert.Equal(t, "session-secret", store.accounts[0].Session["zsxq_access_token"])
	require.Len(t, store.sources, 1)
	assert.Equal(t, "zsxq:planet:9", store.sources[0].ID)
	assert.Equal(t, "8", store.sources[0].OwnerID)
	requestsMu.Lock()
	gotRequests := append([]string(nil), requests...)
	requestsMu.Unlock()
	assert.Equal(t, []string{"/v3/verify_codes", "/v3/access_tokens", "/v3/users/self", "/v2/groups"}, gotRequests)
}

func TestLoginTransactionAttemptAndExpiryLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, *LoginManager, LoginTransaction, *time.Time)
	}{
		{name: "expires after ten minutes", run: func(t *testing.T, manager *LoginManager, transaction LoginTransaction, now *time.Time) {
			*now = now.Add(LoginTransactionTTL)
			_, err := manager.SubmitCode(t.Context(), transaction.ID, "bad")
			assert.ErrorIs(t, err, ErrLoginExpired)
		}},
		{name: "allows at most five submissions", run: func(t *testing.T, manager *LoginManager, transaction LoginTransaction, _ *time.Time) {
			for range MaxCodeAttempts {
				_, err := manager.SubmitCode(t.Context(), transaction.ID, "bad")
				require.Error(t, err)
			}
			_, err := manager.SubmitCode(t.Context(), transaction.ID, "bad")
			assert.ErrorIs(t, err, ErrAttemptsExceeded)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v3/verify_codes" {
					writeEnvelope(t, w, map[string]any{})
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL+"/v2"))
			require.NoError(t, err)
			manager, err := NewLoginManager(client, &loginStoreStub{})
			require.NoError(t, err)
			now := time.Date(2026, time.August, 10, 2, 0, 0, 0, time.UTC)
			manager.SetClockForTest(func() time.Time { return now })
			transaction, err := manager.SendCode(t.Context(), SMSCodeRequest{CountryCode: "+86", Phone: "13800138000", CaptchaVerifyParam: "captcha", AgreementAccepted: true})
			require.NoError(t, err)
			tt.run(t, manager, transaction, &now)
		})
	}
}

type loginStoreStub struct {
	accounts []model.PlatformAccount
	sources  []model.Source
}

func (store *loginStoreStub) PutPlatformAccount(account model.PlatformAccount) error {
	store.accounts = append(store.accounts, account)
	return nil
}

func (store *loginStoreStub) MergeVisibleSources(_ model.Platform, sources []model.Source) error {
	store.sources = append(store.sources, sources...)
	return nil
}

func writeEnvelope(t *testing.T, writer http.ResponseWriter, data any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"succeeded": true, "code": 0, "resp_data": data}))
}

func TestMeAcceptsUIDField(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		id      string
		wantErr error
	}{
		{name: "uid", body: `{"succeeded":true,"code":0,"resp_data":{"user":{"uid":42,"name":"Member"}}}`, id: "42"},
		{name: "user_id fallback", body: `{"succeeded":true,"code":0,"resp_data":{"user":{"user_id":7,"name":"Legacy"}}}`, id: "7"},
		{name: "missing identity is schema drift", body: `{"succeeded":true,"code":0,"resp_data":{"user":{"name":"NoID"}}}`, wantErr: ErrSchemaDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL+"/v2"))
			require.NoError(t, err)
			user, err := client.Me(t.Context())
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.id, user.ID)
		})
	}
}

func TestClassifyBusinessCodesFromEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want error
	}{
		{name: "invalid phone", body: `{"succeeded":false,"code":1004,"resp_data":{}}`, want: ErrInvalidPhone},
		{name: "phone unbound", body: `{"succeeded":false,"code":1031,"resp_data":{}}`, want: ErrPhoneUnbound},
		{name: "phone unbound alias", body: `{"succeeded":false,"code":10013,"resp_data":{}}`, want: ErrPhoneUnbound},
		{name: "invalid code", body: `{"succeeded":false,"code":10022,"resp_data":{}}`, want: ErrInvalidCode},
		{name: "consumed code", body: `{"succeeded":false,"code":90012,"resp_data":{}}`, want: ErrInvalidCode},
		{name: "rate limited", body: `{"succeeded":false,"code":429,"resp_data":{}}`, want: ErrRateLimited},
		{name: "risk control", body: `{"succeeded":false,"code":1006,"resp_data":{}}`, want: ErrRiskControl},
		{name: "authentication", body: `{"succeeded":false,"code":1059,"resp_data":{}}`, want: ErrAuthentication},
		{name: "zero code upstream", body: `{"succeeded":false,"code":0,"resp_data":{}}`, want: ErrUpstream},
		{name: "unknown business code", body: `{"succeeded":false,"code":55555,"resp_data":{}}`, want: ErrUpstream},
		{name: "legacy succeed string false", body: `{"succeed":"false","code":1059}`, want: ErrAuthentication},
		{name: "true string succeeds with empty data", body: `{"succeed":"true","code":0}`, want: ErrSchemaDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client, err := New(server.Client(), "test", WithBaseURL(server.URL+"/v2"))
			require.NoError(t, err)
			_, err = client.Me(t.Context())
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
			assert.NotContains(t, err.Error(), tt.body)
		})
	}
}

func Example_publicError() {
	fmt.Println(publicError(errors.Join(ErrAuthentication, errors.New("phone=13800138000"))))
	// Output: authentication expired
}
