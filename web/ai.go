package web

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
)

var bvidPattern = regexp.MustCompile(`^BV[0-9A-Za-z]{10}$`)

type aiProfileInput struct {
	Name               string              `json:"name"`
	Kind               model.AIProfileKind `json:"kind"`
	BaseURL            string              `json:"base_url"`
	Model              string              `json:"model"`
	APIKey             string              `json:"api_key,omitempty"`
	Language           string              `json:"language,omitempty"`
	Prompt             string              `json:"prompt,omitempty"`
	Temperature        float64             `json:"temperature,omitempty"`
	MaxOutputTokens    int64               `json:"max_output_tokens,omitempty"`
	ContextWindowChars int                 `json:"context_window_chars,omitempty"`
	TimeoutSec         int                 `json:"timeout_sec"`
	Enabled            *bool               `json:"enabled"`
	Default            bool                `json:"default"`
}

type aiProfileView struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Kind               model.AIProfileKind `json:"kind"`
	BaseURL            string              `json:"base_url"`
	Model              string              `json:"model"`
	Language           string              `json:"language,omitempty"`
	Prompt             string              `json:"prompt,omitempty"`
	Temperature        float64             `json:"temperature,omitempty"`
	MaxOutputTokens    int64               `json:"max_output_tokens,omitempty"`
	ContextWindowChars int                 `json:"context_window_chars,omitempty"`
	TimeoutSec         int                 `json:"timeout_sec"`
	Enabled            bool                `json:"enabled"`
	Default            bool                `json:"default"`
	ConfiguredSecrets  []string            `json:"configured_secrets"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

func aiProfileViewFor(profile model.AIProfile) aiProfileView {
	secrets := []string{}
	if profile.APIKey != "" {
		secrets = append(secrets, "api_key")
	}
	return aiProfileView{
		ID: profile.ID, Name: profile.Name, Kind: profile.Kind, BaseURL: profile.BaseURL, Model: profile.Model,
		Language: profile.Language, Prompt: profile.Prompt, Temperature: profile.Temperature,
		MaxOutputTokens: profile.MaxOutputTokens, ContextWindowChars: profile.ContextWindowChars,
		TimeoutSec: profile.TimeoutSec, Enabled: profile.Enabled, Default: profile.Default, ConfiguredSecrets: secrets,
		CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt,
	}
}

func profileFromInput(input aiProfileInput) model.AIProfile {
	profile := model.AIProfile{
		Name: input.Name, Kind: input.Kind, BaseURL: input.BaseURL, Model: input.Model, APIKey: input.APIKey,
		Language: input.Language, Prompt: input.Prompt, Temperature: input.Temperature,
		MaxOutputTokens: input.MaxOutputTokens, ContextWindowChars: input.ContextWindowChars,
		TimeoutSec: input.TimeoutSec, Enabled: input.Enabled != nil && *input.Enabled, Default: input.Default,
	}
	if profile.TimeoutSec == 0 {
		profile.TimeoutSec = 600
	}
	if profile.Kind == model.AIProfileText {
		if profile.ContextWindowChars == 0 {
			profile.ContextWindowChars = 100000
		}
	}
	return profile
}

func (s *Server) aiStatusAPI(w http.ResponseWriter, _ *http.Request) {
	if s.ai == nil {
		writeJSON(w, http.StatusOK, service.AIWorkerStatus{LastError: "AI Worker 未配置"})
		return
	}
	writeJSON(w, http.StatusOK, s.ai.Status())
}

func (s *Server) listAIProfilesAPI(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.store.WithContext(r.Context()).ListAIProfiles()
	views := make([]aiProfileView, 0, len(profiles))
	for _, profile := range profiles {
		views = append(views, aiProfileViewFor(profile))
	}
	s.writeAPIResult(w, http.StatusOK, views, err)
}

func (s *Server) createAIProfileAPI(w http.ResponseWriter, r *http.Request) {
	var input aiProfileInput
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		s.writeAPIResult(w, http.StatusCreated, nil, validationFailure(errors.New("enabled is required")))
		return
	}
	profile, err := s.store.WithContext(r.Context()).PutAIProfile(profileFromInput(input))
	if err != nil {
		err = validationFailure(err)
	} else {
		setAuditResourceID(r, profile.ID)
		s.events.Publish(service.TopicAIStatus)
	}
	s.writeAPIResult(w, http.StatusCreated, aiProfileViewFor(profile), err)
}

func (s *Server) updateAIProfileAPI(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.WithContext(r.Context()).AIProfile(r.PathValue("id"))
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	var input aiProfileInput
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(errors.New("enabled is required")))
		return
	}
	next := profileFromInput(input)
	next.ID, next.CreatedAt = current.ID, current.CreatedAt
	if next.APIKey == "" {
		next.APIKey = current.APIKey
	}
	next, err = s.store.WithContext(r.Context()).PutAIProfile(next)
	if err != nil {
		err = validationFailure(err)
	} else {
		s.events.Publish(service.TopicAIStatus)
	}
	s.writeAPIResult(w, http.StatusOK, aiProfileViewFor(next), err)
}

func (s *Server) deleteAIProfileAPI(w http.ResponseWriter, r *http.Request) {
	err := s.store.WithContext(r.Context()).DeleteAIProfile(r.PathValue("id"))
	if err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, conflictFailure(err))
		return
	}
	s.events.Publish(service.TopicAIStatus)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateAIProfileAvailabilityAPI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(errors.New("enabled is required")))
		return
	}
	profile, err := s.store.WithContext(r.Context()).SetAIProfileEnabled(r.PathValue("id"), *input.Enabled)
	if err == nil {
		s.events.Publish(service.TopicAIStatus)
	}
	s.writeAPIResult(w, http.StatusOK, aiProfileViewFor(profile), err)
}

func (s *Server) testAIProfileAPI(w http.ResponseWriter, r *http.Request) {
	profile, err := s.store.WithContext(r.Context()).AIProfile(r.PathValue("id"))
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	if s.ai == nil {
		writeJSON(w, http.StatusOK, service.AIProfileTestResult{Message: "AI Worker 不可用", ErrorCode: "worker_unavailable"})
	} else {
		writeJSON(w, http.StatusOK, s.ai.TestProfile(r.Context(), profile))
	}
}

func (s *Server) listAIPromptsAPI(w http.ResponseWriter, r *http.Request) {
	prompts, err := s.store.WithContext(r.Context()).ListAIPrompts()
	s.writeAPIResult(w, http.StatusOK, prompts, err)
}

func (s *Server) createAIPromptAPI(w http.ResponseWriter, r *http.Request) {
	var prompt model.AIPromptTemplate
	if !decodeAPIRequest(w, r, &prompt) {
		return
	}
	prompt.ID = ""
	saved, err := s.store.WithContext(r.Context()).PutAIPrompt(prompt)
	if err != nil {
		err = validationFailure(err)
	} else {
		setAuditResourceID(r, saved.ID)
		s.events.Publish(service.TopicAIStatus)
	}
	s.writeAPIResult(w, http.StatusCreated, saved, err)
}

func (s *Server) updateAIPromptAPI(w http.ResponseWriter, r *http.Request) {
	var prompt model.AIPromptTemplate
	if !decodeAPIRequest(w, r, &prompt) {
		return
	}
	prompt.ID = r.PathValue("id")
	saved, err := s.store.WithContext(r.Context()).PutAIPrompt(prompt)
	if err != nil {
		err = validationFailure(err)
	} else {
		s.events.Publish(service.TopicAIStatus)
	}
	s.writeAPIResult(w, http.StatusOK, saved, err)
}

func (s *Server) deleteAIPromptAPI(w http.ResponseWriter, r *http.Request) {
	err := s.store.WithContext(r.Context()).DeleteAIPrompt(r.PathValue("id"))
	if err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, conflictFailure(err))
		return
	}
	s.events.Publish(service.TopicAIStatus)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createAITranscriptionAPI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ClientRequestID string `json:"client_request_id"`
		BVID            string `json:"bvid"`
		Page            int    `json:"page,omitempty"`
		ProfileID       string `json:"profile_id"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	input.BVID = strings.TrimSpace(input.BVID)
	if !bvidPattern.MatchString(input.BVID) || input.Page < 0 {
		s.writeAPIResult(w, http.StatusAccepted, nil, validationFailure(errors.New("invalid BVID or page")))
		return
	}
	profile, err := s.store.WithContext(r.Context()).AIProfile(input.ProfileID)
	if err != nil || profile.Kind != model.AIProfileTranscription {
		s.writeAPIResult(w, http.StatusAccepted, nil, validationFailure(errors.New("transcription profile is required")))
		return
	}
	job, created, err := s.store.WithContext(r.Context()).CreateAIJob(model.AIJob{
		ClientRequestID: input.ClientRequestID, Kind: model.AIJobTranscription, ProfileID: input.ProfileID,
		Input: model.AITranscriptionInput{BVID: input.BVID, Page: input.Page},
	})
	if err == nil {
		setAuditResourceID(r, job.ID)
		if created && s.ai != nil {
			s.ai.Notify()
		}
	}
	s.writeAPIResult(w, http.StatusAccepted, job, err)
}

func (s *Server) createAISummaryAPI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ClientRequestID    string `json:"client_request_id"`
		Text               string `json:"text,omitempty"`
		TranscriptionJobID string `json:"transcription_job_id,omitempty"`
		ProfileID          string `json:"profile_id"`
		PromptID           string `json:"prompt_id"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	input.Text = strings.TrimSpace(input.Text)
	if (input.Text == "") == (input.TranscriptionJobID == "") {
		s.writeAPIResult(w, http.StatusAccepted, nil, validationFailure(errors.New("provide exactly one of text or transcription_job_id")))
		return
	}
	profile, err := s.store.WithContext(r.Context()).AIProfile(input.ProfileID)
	if err != nil || profile.Kind != model.AIProfileText {
		s.writeAPIResult(w, http.StatusAccepted, nil, validationFailure(errors.New("text profile is required")))
		return
	}
	job, created, err := s.store.WithContext(r.Context()).CreateAIJob(model.AIJob{
		ClientRequestID: input.ClientRequestID, Kind: model.AIJobSummary, ProfileID: input.ProfileID, PromptID: input.PromptID,
		Input: model.AISummaryInput{Text: input.Text, TranscriptionID: input.TranscriptionJobID},
	})
	if err == nil {
		setAuditResourceID(r, job.ID)
		if created && s.ai != nil {
			s.ai.Notify()
		}
	}
	s.writeAPIResult(w, http.StatusAccepted, job, err)
}

func (s *Server) listAIJobsAPI(w http.ResponseWriter, r *http.Request) {
	limit, err := optionalPositiveInt(r.URL.Query().Get("limit"), 20)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer")
		return
	}
	offset, err := optionalPositiveInt(r.URL.Query().Get("offset"), 0)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "offset must be an integer")
		return
	}
	page, queryErr := s.store.WithContext(r.Context()).ListAIJobs(model.AIJobQuery{
		Kind: model.AIJobKind(r.URL.Query().Get("kind")), State: model.AIJobState(r.URL.Query().Get("state")), Limit: limit, Offset: offset,
	})
	s.writeAPIResult(w, http.StatusOK, page, queryErr)
}

func optionalPositiveInt(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 0 {
		return 0, errors.New("invalid integer")
	}
	return number, nil
}

func (s *Server) getAIJobAPI(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.WithContext(r.Context()).AIJob(r.PathValue("id"))
	s.writeAPIResult(w, http.StatusOK, job, err)
}

func (s *Server) cancelAIJobAPI(w http.ResponseWriter, r *http.Request) {
	err := state.ErrAIJobNotCancelable
	if s.ai != nil {
		err = s.ai.CancelJob(r.PathValue("id"))
	}
	s.writeAPIResult(w, http.StatusAccepted, map[string]string{"status": "canceled"}, classifyAIConflict(err))
}

func (s *Server) retryAIJobAPI(w http.ResponseWriter, r *http.Request) {
	err := s.store.WithContext(r.Context()).RetryAIJob(r.PathValue("id"))
	if err == nil && s.ai != nil {
		s.ai.Notify()
	}
	s.writeAPIResult(w, http.StatusAccepted, map[string]string{"status": "queued"}, classifyAIConflict(err))
}

func (s *Server) deleteAIJobAPI(w http.ResponseWriter, r *http.Request) {
	err := s.store.WithContext(r.Context()).DeleteAIJob(r.PathValue("id"))
	if err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, classifyAIConflict(err))
		return
	}
	s.events.Publish(service.TopicAIJobs)
	w.WriteHeader(http.StatusNoContent)
}

func classifyAIConflict(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, state.ErrAIJobNotCancelable) || errors.Is(err, state.ErrAIJobNotRetryable) || errors.Is(err, state.ErrAIJobNotTerminal) {
		return conflictFailure(err)
	}
	return err
}
