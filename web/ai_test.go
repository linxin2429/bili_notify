package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAIManagementAPI(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, http.DefaultClient)

	response := fixture.request(t, http.MethodGet, "/api/v3/ai/status", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "AI Worker 未配置")

	profileBody := map[string]any{"name": "transcribe", "kind": "transcription", "base_url": "https://openrouter.ai/api/v1", "model": "openai/gpt-transcribe", "api_key": "secret-key", "language": "zh", "timeout_sec": 600, "enabled": true, "default": true}
	response = fixture.request(t, http.MethodPost, "/api/v3/ai/profiles", profileBody, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var profile aiProfileView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &profile))
	assert.Equal(t, []string{"api_key"}, profile.ConfiguredSecrets)
	assert.NotContains(t, response.Body.String(), "secret-key")

	profileBody["name"] = "renamed"
	profileBody["api_key"] = ""
	response = fixture.request(t, http.MethodPut, "/api/v3/ai/profiles/"+profile.ID, profileBody, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	saved, err := fixture.store.AIProfile(profile.ID)
	require.NoError(t, err)
	assert.Equal(t, "secret-key", saved.APIKey)
	response = fixture.request(t, http.MethodPut, "/api/v3/ai/profiles/"+profile.ID+"/availability", map[string]any{"enabled": false}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	saved, err = fixture.store.AIProfile(profile.ID)
	require.NoError(t, err)
	assert.False(t, saved.Enabled)
	assert.False(t, saved.Default)
	response = fixture.request(t, http.MethodPost, "/api/v3/ai/profiles/"+profile.ID+"/test", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "worker_unavailable")
	fixture.server.ai = service.NewAIEngine(fixture.store, "", fixture.server.logger, fixture.events)

	textProfile, err := fixture.store.PutAIProfile(model.AIProfile{Name: "summary", Kind: model.AIProfileText, BaseURL: "https://openrouter.ai/api/v1", Model: "openai/gpt-5-mini", APIKey: "text-key", Temperature: 0.2, MaxOutputTokens: 4096, ContextWindowChars: 100000, TimeoutSec: 600, Enabled: true, Default: true})
	require.NoError(t, err)
	promptBody := map[string]any{"name": "default", "system_prompt": "system", "chunk_prompt": "chunk {{text}}", "reduce_prompt": "reduce {{summaries}}", "default": true}
	response = fixture.request(t, http.MethodPost, "/api/v3/ai/prompts", promptBody, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var prompt model.AIPromptTemplate
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &prompt))
	promptBody["name"] = "updated"
	response = fixture.request(t, http.MethodPut, "/api/v3/ai/prompts/"+prompt.ID, promptBody, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updatedPrompt model.AIPromptTemplate
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &updatedPrompt))
	assert.Equal(t, "updated", updatedPrompt.Name)

	response = fixture.request(t, http.MethodPost, "/api/v3/ai/transcriptions", map[string]any{"client_request_id": "transcription-1", "bvid": "BV1xx411c7mD", "profile_id": profile.ID}, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var transcription model.AIJob
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &transcription))
	response = fixture.request(t, http.MethodPost, "/api/v3/ai/summaries", map[string]any{"client_request_id": "summary-1", "text": "content", "profile_id": textProfile.ID, "prompt_id": prompt.ID}, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var summary model.AIJob
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &summary))

	response = fixture.request(t, http.MethodGet, "/api/v3/ai/jobs?limit=10", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), summary.ID)
	response = fixture.request(t, http.MethodGet, "/api/v3/ai/jobs/"+transcription.ID, nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "BV1xx411c7mD")
	response = fixture.request(t, http.MethodPost, "/api/v3/ai/jobs/"+transcription.ID+"/cancel", nil, true)
	assert.Equal(t, http.StatusAccepted, response.Code, response.Body.String())

	require.NoError(t, fixture.store.CancelAIJob(summary.ID))
	response = fixture.request(t, http.MethodPost, "/api/v3/ai/jobs/"+summary.ID+"/retry", nil, true)
	assert.Equal(t, http.StatusAccepted, response.Code)
	require.NoError(t, fixture.store.CancelAIJob(summary.ID))
	response = fixture.request(t, http.MethodDelete, "/api/v3/ai/jobs/"+summary.ID, nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code)

	response = fixture.request(t, http.MethodGet, "/api/v3/ai/profiles", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	response = fixture.request(t, http.MethodGet, "/api/v3/ai/prompts", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)

	spareProfile, err := fixture.store.PutAIProfile(model.AIProfile{Name: "spare", Kind: model.AIProfileTranscription, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", TimeoutSec: 60, Enabled: true})
	require.NoError(t, err)
	response = fixture.request(t, http.MethodDelete, "/api/v3/ai/profiles/"+spareProfile.ID, nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	sparePrompt, err := fixture.store.PutAIPrompt(model.AIPromptTemplate{Name: "spare", ChunkPrompt: "{{text}}", ReducePrompt: "{{summaries}}"})
	require.NoError(t, err)
	response = fixture.request(t, http.MethodDelete, "/api/v3/ai/prompts/"+sparePrompt.ID, nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
}

func TestAIProfileMaxOutputTokensAPI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		value       any
		omitEnabled bool
		status      int
	}{
		{name: "null uses provider default", value: nil, status: http.StatusCreated},
		{name: "zero uses provider default", value: 0, status: http.StatusCreated},
		{name: "above former limit", value: int64(1 << 40), status: http.StatusCreated},
		{name: "negative rejected", value: -1, status: http.StatusBadRequest},
		{name: "missing enabled rejected", value: 1, omitEnabled: true, status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAdminAPIFixture(t, http.DefaultClient)
			body := map[string]any{"name": tt.name, "kind": "text", "base_url": "https://provider.example/v1", "model": "model", "api_key": "secret", "temperature": 0.2, "max_output_tokens": tt.value, "context_window_chars": 10000, "timeout_sec": 60, "enabled": true, "default": false}
			if tt.omitEnabled {
				delete(body, "enabled")
			}
			response := fixture.request(t, http.MethodPost, "/api/v3/ai/profiles", body, true)
			assert.Equal(t, tt.status, response.Code, response.Body.String())
		})
	}
}

func TestAIJobRequestValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		body map[string]any
	}{
		{name: "invalid bvid", path: "/api/v3/ai/transcriptions", body: map[string]any{"client_request_id": "a", "bvid": "bad", "profile_id": "missing"}},
		{name: "ambiguous summary source", path: "/api/v3/ai/summaries", body: map[string]any{"client_request_id": "b", "text": "text", "transcription_job_id": "job", "profile_id": "missing", "prompt_id": "missing"}},
		{name: "missing transcription profile", path: "/api/v3/ai/transcriptions", body: map[string]any{"client_request_id": "c", "bvid": "BV1xx411c7mD", "profile_id": "missing"}},
		{name: "missing summary profile", path: "/api/v3/ai/summaries", body: map[string]any{"client_request_id": "d", "text": "text", "profile_id": "missing", "prompt_id": "missing"}},
		{name: "invalid list limit", path: "/api/v3/ai/jobs?limit=invalid"},
		{name: "negative list offset", path: "/api/v3/ai/jobs?offset=-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAdminAPIFixture(t, http.DefaultClient)
			method, csrf := http.MethodGet, false
			if tt.body != nil {
				method, csrf = http.MethodPost, true
			}
			response := fixture.request(t, method, tt.path, tt.body, csrf)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
}

func TestAIProfileInputDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		kind                   model.AIProfileKind
		wantMaxOutputTokens    int64
		wantContextWindowChars int
	}{
		{name: "transcription", kind: model.AIProfileTranscription},
		{name: "text", kind: model.AIProfileText, wantContextWindowChars: 100000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := profileFromInput(aiProfileInput{Kind: tt.kind})
			assert.Equal(t, 600, profile.TimeoutSec)
			assert.Equal(t, tt.wantMaxOutputTokens, profile.MaxOutputTokens)
			assert.Equal(t, tt.wantContextWindowChars, profile.ContextWindowChars)
		})
	}
}

func TestAIMutationConflicts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "delete missing profile", method: http.MethodDelete, path: "/api/v3/ai/profiles/missing"},
		{name: "delete missing prompt", method: http.MethodDelete, path: "/api/v3/ai/prompts/missing"},
		{name: "cancel missing job", method: http.MethodPost, path: "/api/v3/ai/jobs/missing/cancel"},
		{name: "retry missing job", method: http.MethodPost, path: "/api/v3/ai/jobs/missing/retry"},
		{name: "delete missing job", method: http.MethodDelete, path: "/api/v3/ai/jobs/missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAdminAPIFixture(t, http.DefaultClient)
			fixture.server.ai = service.NewAIEngine(fixture.store, "", fixture.server.logger, fixture.events)
			response := fixture.request(t, tt.method, tt.path, nil, true)
			assert.Equal(t, http.StatusConflict, response.Code, response.Body.String())
		})
	}
}
