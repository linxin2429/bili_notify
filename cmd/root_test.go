package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeFlagBinding(t *testing.T) {
	t.Parallel()
	root := NewRootCmd()
	root.SetArgs([]string{"serve", "--request-rate", "0"})
	err := root.ExecuteContext(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request rate")
}

func TestHealthcheckCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		body    string
		args    []string
		wantErr string
	}{
		{
			name:   "default liveness probe",
			status: http.StatusOK,
			body:   `{"status":"ok"}`,
			args:   []string{"healthcheck"},
		},
		{
			name:   "contains match",
			status: http.StatusOK,
			body:   `{"setup_required":true}`,
			args:   []string{"healthcheck", "--contains", `"setup_required":true`},
		},
		{
			name:    "contains miss",
			status:  http.StatusOK,
			body:    `{"setup_required":false}`,
			args:    []string{"healthcheck", "--contains", `"setup_required":true`},
			wantErr: "does not contain",
		},
		{
			name:    "non-success status",
			status:  http.StatusServiceUnavailable,
			body:    `{"ready":false}`,
			args:    []string{"healthcheck"},
			wantErr: "HTTP 503",
		},
		{
			name:   "post with body",
			status: http.StatusOK,
			body:   `{}`,
			args:   []string{"healthcheck", "--method", "POST", "--body", `{"password":"x"}`},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.name == "post with body" {
					assert.Equal(t, http.MethodPost, r.Method)
					assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			root := NewRootCmd()
			root.SetArgs(append(tt.args, "--url", server.URL))
			err := root.ExecuteContext(t.Context())
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
