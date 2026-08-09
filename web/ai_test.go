package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAIManagementAPI(t *testing.T) {
	t.Parallel()
	fixture := newAdminAPIFixture(t, http.DefaultClient)

	response := fixture.request(t, http.MethodGet, "/api/v2/ai/status", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "AI Worker 未配置")

	profileBody := map[string]any{"name": "transcribe", "kind": "transcription", "base_url": "https://openrouter.ai/api/v1", "model": "openai/gpt-transcribe", "api_key": "secret-key", "language": "zh", "timeout_sec": 600, "default": true}
	response = fixture.request(t, http.MethodPost, "/api/v2/ai/profiles", profileBody, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var profile aiProfileView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &profile))
	assert.Equal(t, []string{"api_key"}, profile.ConfiguredSecrets)
	assert.NotContains(t, response.Body.String(), "secret-key")

	profileBody["name"] = "renamed"
	profileBody["api_key"] = ""
	response = fixture.request(t, http.MethodPut, "/api/v2/ai/profiles/"+profile.ID, profileBody, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	saved, err := fixture.store.AIProfile(profile.ID)
	require.NoError(t, err)
	assert.Equal(t, "secret-key", saved.APIKey)
	response = fixture.request(t, http.MethodPost, "/api/v2/ai/profiles/"+profile.ID+"/test", nil, true)
	assert.Equal(t, http.StatusBadGateway, response.Code)

	textProfile, err := fixture.store.PutAIProfile(model.AIProfile{Name: "summary", Kind: model.AIProfileText, BaseURL: "https://openrouter.ai/api/v1", Model: "openai/gpt-5-mini", APIKey: "text-key", Temperature: 0.2, MaxOutputTokens: 4096, ContextWindowChars: 100000, TimeoutSec: 600, Default: true})
	require.NoError(t, err)
	promptBody := map[string]any{"name": "default", "system_prompt": "system", "chunk_prompt": "chunk {{text}}", "reduce_prompt": "reduce {{summaries}}", "default": true}
	response = fixture.request(t, http.MethodPost, "/api/v2/ai/prompts", promptBody, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var prompt model.AIPromptTemplate
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &prompt))

	response = fixture.request(t, http.MethodPost, "/api/v2/ai/transcriptions", map[string]any{"client_request_id": "transcription-1", "bvid": "BV1xx411c7mD", "profile_id": profile.ID}, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var transcription model.AIJob
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &transcription))
	response = fixture.request(t, http.MethodPost, "/api/v2/ai/summaries", map[string]any{"client_request_id": "summary-1", "text": "content", "profile_id": textProfile.ID, "prompt_id": prompt.ID}, true)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	var summary model.AIJob
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &summary))

	response = fixture.request(t, http.MethodGet, "/api/v2/ai/jobs?limit=10", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), summary.ID)
	response = fixture.request(t, http.MethodGet, "/api/v2/ai/jobs/"+transcription.ID, nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "BV1xx411c7mD")

	require.NoError(t, fixture.store.CancelAIJob(summary.ID))
	response = fixture.request(t, http.MethodPost, "/api/v2/ai/jobs/"+summary.ID+"/retry", nil, true)
	assert.Equal(t, http.StatusAccepted, response.Code)
	require.NoError(t, fixture.store.CancelAIJob(summary.ID))
	response = fixture.request(t, http.MethodDelete, "/api/v2/ai/jobs/"+summary.ID, nil, true)
	assert.Equal(t, http.StatusNoContent, response.Code)

	response = fixture.request(t, http.MethodGet, "/api/v2/ai/profiles", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
	response = fixture.request(t, http.MethodGet, "/api/v2/ai/prompts", nil, false)
	assert.Equal(t, http.StatusOK, response.Code)
}

func TestAIJobRequestValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		body map[string]any
	}{
		{name: "invalid bvid", path: "/api/v2/ai/transcriptions", body: map[string]any{"client_request_id": "a", "bvid": "bad", "profile_id": "missing"}},
		{name: "ambiguous summary source", path: "/api/v2/ai/summaries", body: map[string]any{"client_request_id": "b", "text": "text", "transcription_job_id": "job", "profile_id": "missing", "prompt_id": "missing"}},
		{name: "negative list offset", path: "/api/v2/ai/jobs?offset=-1"},
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
