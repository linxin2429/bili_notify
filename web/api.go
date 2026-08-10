package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
)

func (s *Server) registerAdminAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v3/runtime", s.requireSession(false, s.runtimeAPI))
	mux.HandleFunc("GET /api/v3/settings", s.requireSession(false, s.settingsAPI))
	mux.HandleFunc("GET /api/v3/channels", s.requireSession(false, s.channelsAPI))
	mux.HandleFunc("POST /api/v3/channels", s.audit("channel.create", "channel", "", s.requireSession(true, s.createChannelAPI)))
	mux.HandleFunc("PUT /api/v3/channels/{id}", s.audit("channel.update", "channel", "id", s.requireSession(true, s.updateChannelAPI)))
	mux.HandleFunc("DELETE /api/v3/channels/{id}", s.audit("channel.delete", "channel", "id", s.requireSession(true, s.deleteChannelAPI)))
	mux.HandleFunc("POST /api/v3/channels/{id}/test", s.audit("channel.test", "channel", "id", s.requireSession(true, s.testChannelAPI)))
	mux.HandleFunc("GET /api/v3/deliveries", s.requireSession(false, s.deliveriesAPI))
	mux.HandleFunc("POST /api/v3/deliveries/{id}/retry", s.audit("delivery.retry", "delivery", "id", s.requireSession(true, s.retryDeliveryAPI)))
	mux.HandleFunc("GET /api/v3/microsoft-logins", s.requireSession(false, s.microsoftLoginsAPI))
	mux.HandleFunc("POST /api/v3/channels/{id}/microsoft-login", s.audit("microsoft.login.start", "channel", "id", s.requireSession(true, s.startMicrosoftLoginAPI)))
	mux.HandleFunc("DELETE /api/v3/channels/{id}/microsoft-login", s.audit("microsoft.login.cancel", "channel", "id", s.requireSession(true, s.cancelMicrosoftLoginAPI)))
	mux.HandleFunc("PUT /api/v3/settings", s.audit("settings.update", "settings", "", s.requireSession(true, s.updateSettingsAPI)))
	mux.HandleFunc("GET /api/v3/audit-logs", s.requireSession(false, s.queryAuditLogsAPI))
	mux.HandleFunc("GET /api/v3/ai/status", s.requireSession(false, s.aiStatusAPI))
	mux.HandleFunc("GET /api/v3/ai/profiles", s.requireSession(false, s.listAIProfilesAPI))
	mux.HandleFunc("POST /api/v3/ai/profiles", s.audit("ai.profile.create", "ai_profile", "", s.requireSession(true, s.createAIProfileAPI)))
	mux.HandleFunc("PUT /api/v3/ai/profiles/{id}", s.audit("ai.profile.update", "ai_profile", "id", s.requireSession(true, s.updateAIProfileAPI)))
	mux.HandleFunc("PUT /api/v3/ai/profiles/{id}/availability", s.audit("ai.profile.availability.update", "ai_profile", "id", s.requireSession(true, s.updateAIProfileAvailabilityAPI)))
	mux.HandleFunc("DELETE /api/v3/ai/profiles/{id}", s.audit("ai.profile.delete", "ai_profile", "id", s.requireSession(true, s.deleteAIProfileAPI)))
	mux.HandleFunc("POST /api/v3/ai/profiles/{id}/test", s.audit("ai.profile.test", "ai_profile", "id", s.requireSession(true, s.testAIProfileAPI)))
	mux.HandleFunc("GET /api/v3/ai/prompts", s.requireSession(false, s.listAIPromptsAPI))
	mux.HandleFunc("POST /api/v3/ai/prompts", s.audit("ai.prompt.create", "ai_prompt", "", s.requireSession(true, s.createAIPromptAPI)))
	mux.HandleFunc("PUT /api/v3/ai/prompts/{id}", s.audit("ai.prompt.update", "ai_prompt", "id", s.requireSession(true, s.updateAIPromptAPI)))
	mux.HandleFunc("DELETE /api/v3/ai/prompts/{id}", s.audit("ai.prompt.delete", "ai_prompt", "id", s.requireSession(true, s.deleteAIPromptAPI)))
	mux.HandleFunc("POST /api/v3/ai/transcriptions", s.audit("ai.transcription.create", "ai_job", "", s.requireSession(true, s.createAITranscriptionAPI)))
	mux.HandleFunc("POST /api/v3/ai/summaries", s.audit("ai.summary.create", "ai_job", "", s.requireSession(true, s.createAISummaryAPI)))
	mux.HandleFunc("GET /api/v3/ai/jobs", s.requireSession(false, s.listAIJobsAPI))
	mux.HandleFunc("GET /api/v3/ai/jobs/{id}", s.requireSession(false, s.getAIJobAPI))
	mux.HandleFunc("POST /api/v3/ai/jobs/{id}/cancel", s.audit("ai.job.cancel", "ai_job", "id", s.requireSession(true, s.cancelAIJobAPI)))
	mux.HandleFunc("POST /api/v3/ai/jobs/{id}/retry", s.audit("ai.job.retry", "ai_job", "id", s.requireSession(true, s.retryAIJobAPI)))
	mux.HandleFunc("DELETE /api/v3/ai/jobs/{id}", s.audit("ai.job.delete", "ai_job", "id", s.requireSession(true, s.deleteAIJobAPI)))
}

type runtimeView struct {
	Status    service.Status `json:"status"`
	Timezone  string         `json:"timezone"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type cursorPage struct {
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

type cursorPageResponse struct {
	Items any        `json:"items"`
	Page  cursorPage `json:"page"`
}

type listCursor struct {
	Version int    `json:"v"`
	Sort    int64  `json:"sort"`
	Key     string `json:"key"`
}

func (s *Server) runtimeAPI(w http.ResponseWriter, r *http.Request) {
	status, err := s.engine.Status()
	s.writeAPIResult(w, http.StatusOK, runtimeView{Status: status, Timezone: localTimezoneName(), UpdatedAt: time.Now()}, err)
}

func (s *Server) settingsAPI(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.settings.Settings())
}

func (s *Server) channelsAPI(w http.ResponseWriter, _ *http.Request) {
	channels, err := s.channelViews()
	s.writeAPIResult(w, http.StatusOK, channels, err)
}

func (s *Server) deliveriesAPI(w http.ResponseWriter, r *http.Request) {
	limit, ok := parsePageLimit(w, r, 20)
	if !ok {
		return
	}
	cursor, ok := parseAfterCursor(w, r)
	if !ok {
		return
	}
	query := state.DeliveryQuery{Limit: limit + 1}
	if cursor != nil {
		query.AfterCreatedAt = time.Unix(cursor.Sort, 0)
		query.AfterID = cursor.Key
	}
	deliveries, err := s.store.WithContext(r.Context()).QueryDeliveries(query)
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	hasMore := len(deliveries) > limit
	if hasMore {
		deliveries = deliveries[:limit]
	}
	next := ""
	if hasMore && len(deliveries) > 0 {
		last := deliveries[len(deliveries)-1]
		next = encodeListCursor(last.CreatedAt.Unix(), last.ID)
	}
	writeJSON(w, http.StatusOK, cursorPageResponse{Items: deliveryViews(deliveries), Page: cursorPage{NextCursor: next, HasMore: hasMore}})
}

func (s *Server) biliLoginAPI(w http.ResponseWriter, _ *http.Request) {
	login, err := s.biliLoginView()
	s.writeAPIResult(w, http.StatusOK, login, err)
}

func (s *Server) microsoftLoginsAPI(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.engine.MicrosoftLogins())
}

func (s *Server) createChannelAPI(w http.ResponseWriter, r *http.Request) {
	var input channelInput
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	channel, err := s.saveChannel(input, "")
	if err == nil {
		setAuditResourceID(r, channel.ID)
		setAuditDetails(r, channelAuditDetails(nil, channel))
		s.events.Publish(service.TopicStatus | service.TopicChannels | service.TopicDeliveries | service.TopicMicrosoftLogin)
	}
	s.writeAPIResult(w, http.StatusCreated, toChannelView(channel), err)
}

func (s *Server) updateChannelAPI(w http.ResponseWriter, r *http.Request) {
	var input channelInput
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	var before *model.Channel
	if current, currentErr := s.store.WithContext(r.Context()).Channel(id); currentErr == nil {
		before = &current
	}
	channel, err := s.saveChannel(input, id)
	if err == nil {
		setAuditDetails(r, channelAuditDetails(before, channel))
		s.events.Publish(service.TopicStatus | service.TopicChannels | service.TopicDeliveries | service.TopicMicrosoftLogin)
	}
	s.writeAPIResult(w, http.StatusOK, toChannelView(channel), err)
}

func (s *Server) deleteChannelAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if channel, err := s.store.WithContext(r.Context()).Channel(id); err == nil {
		setAuditDetails(r, channelAuditDetails(&channel, model.Channel{}))
	}
	if err := s.store.WithContext(r.Context()).DeleteChannel(id); err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	s.engine.CancelMicrosoftLogin(id)
	s.events.Publish(service.TopicStatus | service.TopicChannels | service.TopicMicrosoftLogin)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testChannelAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	if err := s.engine.TestChannel(ctx, r.PathValue("id")); err != nil {
		writeAPIError(w, http.StatusBadGateway, "upstream_failure", "notification channel test failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

func (s *Server) retryDeliveryAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.writeAPIResult(w, http.StatusAccepted, nil, validationFailure(errors.New("delivery id is required")))
		return
	}
	if err := s.store.WithContext(r.Context()).RetryDelivery(id, time.Now()); err != nil {
		if errors.Is(err, state.ErrDeliveryNotBlocked) {
			err = conflictFailure(err)
		}
		s.writeAPIResult(w, http.StatusAccepted, nil, err)
		return
	}
	s.events.Publish(service.TopicDeliveries)
	s.writeAPIResult(w, http.StatusAccepted, map[string]string{"status": "queued"}, nil)
}

func (s *Server) startBiliLoginAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	login, err := s.engine.StartLogin(ctx)
	if err != nil {
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
	setAuditResourceID(r, login.Key)
	view, err := s.biliLoginViewFor(login)
	s.writeAPIResult(w, http.StatusCreated, view, err)
}

func (s *Server) cancelBiliLoginAPI(w http.ResponseWriter, r *http.Request) {
	s.engine.CancelLogin(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) startMicrosoftLoginAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	login, err := s.engine.StartMicrosoftLogin(ctx, r.PathValue("id"))
	s.writeAPIResult(w, http.StatusCreated, login, err)
}

func (s *Server) cancelMicrosoftLoginAPI(w http.ResponseWriter, r *http.Request) {
	s.engine.CancelMicrosoftLogin(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

type runtimeSettingsRequest struct {
	BilibiliDynamicIntervalSec *int     `json:"bilibili_dynamic_interval_sec"`
	BilibiliRequestRate        *float64 `json:"bilibili_request_rate"`
	BilibiliRequestConcurrency *int     `json:"bilibili_request_concurrency"`
	BilibiliCommentsEnabled    *bool    `json:"bilibili_comments_enabled"`
	BilibiliCommentTrackN      *int     `json:"bilibili_comment_track_n"`
	BilibiliCommentIntervalSec *int     `json:"bilibili_comment_interval_sec"`
	BilibiliRelationRefreshSec *int     `json:"bilibili_relation_refresh_interval_sec"`
	BilibiliSpaceReconcileSec  *int     `json:"bilibili_space_reconcile_interval_sec"`
	BilibiliMaxDynamicPages    *int     `json:"bilibili_max_dynamic_pages"`
	BilibiliRiskPauseSec       *int     `json:"bilibili_risk_pause_sec"`
	ZSXQDynamicIntervalSec     *int     `json:"zsxq_dynamic_interval_sec"`
	ZSXQCommentIntervalSec     *int     `json:"zsxq_comment_interval_sec"`
	ZSXQCommentsEnabled        *bool    `json:"zsxq_comments_enabled"`
	ZSXQRequestRate            *float64 `json:"zsxq_request_rate"`
	ZSXQRequestConcurrency     *int     `json:"zsxq_request_concurrency"`
	ZSXQRiskPauseSec           *int     `json:"zsxq_risk_pause_sec"`
	ZSXQAssetMaxFileMiB        *int     `json:"zsxq_asset_max_file_mib"`
	ZSXQAssetTotalBudgetGiB    *int     `json:"zsxq_asset_total_budget_gib"`
	LogLevel                   *string  `json:"log_level"`
	AuditLogRetentionDays      *int     `json:"audit_log_retention_days"`
	DeliveryConcurrency        *int     `json:"delivery_concurrency"`
	BacklogAlertCount          *int     `json:"backlog_alert_count"`
	BacklogAlertAgeSec         *int     `json:"backlog_alert_age_sec"`
	DeliveryRetryDelaysSec     *[]int   `json:"delivery_retry_delays_sec"`
	AIAutoProcessingEnabled    *bool    `json:"ai_auto_processing_enabled"`
}

func (input runtimeSettingsRequest) settings() (model.RuntimeSettings, error) {
	missing := make([]string, 0)
	for name, present := range map[string]bool{
		"bilibili_dynamic_interval_sec": input.BilibiliDynamicIntervalSec != nil, "bilibili_request_rate": input.BilibiliRequestRate != nil,
		"bilibili_request_concurrency": input.BilibiliRequestConcurrency != nil, "bilibili_comments_enabled": input.BilibiliCommentsEnabled != nil,
		"bilibili_comment_track_n": input.BilibiliCommentTrackN != nil, "bilibili_comment_interval_sec": input.BilibiliCommentIntervalSec != nil,
		"bilibili_relation_refresh_interval_sec": input.BilibiliRelationRefreshSec != nil,
		"bilibili_space_reconcile_interval_sec":  input.BilibiliSpaceReconcileSec != nil, "bilibili_max_dynamic_pages": input.BilibiliMaxDynamicPages != nil,
		"bilibili_risk_pause_sec":   input.BilibiliRiskPauseSec != nil,
		"zsxq_dynamic_interval_sec": input.ZSXQDynamicIntervalSec != nil, "zsxq_comment_interval_sec": input.ZSXQCommentIntervalSec != nil,
		"zsxq_comments_enabled": input.ZSXQCommentsEnabled != nil, "zsxq_request_rate": input.ZSXQRequestRate != nil,
		"zsxq_request_concurrency": input.ZSXQRequestConcurrency != nil, "zsxq_risk_pause_sec": input.ZSXQRiskPauseSec != nil,
		"zsxq_asset_max_file_mib": input.ZSXQAssetMaxFileMiB != nil, "zsxq_asset_total_budget_gib": input.ZSXQAssetTotalBudgetGiB != nil,
		"log_level": input.LogLevel != nil, "audit_log_retention_days": input.AuditLogRetentionDays != nil,
		"delivery_concurrency": input.DeliveryConcurrency != nil,
		"backlog_alert_count":  input.BacklogAlertCount != nil, "backlog_alert_age_sec": input.BacklogAlertAgeSec != nil,
		"delivery_retry_delays_sec":  input.DeliveryRetryDelaysSec != nil,
		"ai_auto_processing_enabled": input.AIAutoProcessingEnabled != nil,
	} {
		if !present {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return model.RuntimeSettings{}, fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}
	if len(*input.DeliveryRetryDelaysSec) != model.DeliveryRetryStages {
		return model.RuntimeSettings{}, fmt.Errorf("delivery_retry_delays_sec must contain exactly %d values", model.DeliveryRetryStages)
	}
	settings := model.RuntimeSettings{
		BilibiliDynamicIntervalSec: *input.BilibiliDynamicIntervalSec, BilibiliRequestRate: *input.BilibiliRequestRate, BilibiliRequestConcurrency: *input.BilibiliRequestConcurrency,
		BilibiliCommentsEnabled: *input.BilibiliCommentsEnabled, BilibiliCommentTrackN: *input.BilibiliCommentTrackN,
		BilibiliCommentIntervalSec: *input.BilibiliCommentIntervalSec, BilibiliRelationRefreshSec: *input.BilibiliRelationRefreshSec,
		BilibiliSpaceReconcileSec: *input.BilibiliSpaceReconcileSec, BilibiliMaxDynamicPages: *input.BilibiliMaxDynamicPages,
		BilibiliRiskPauseSec:   *input.BilibiliRiskPauseSec,
		ZSXQDynamicIntervalSec: *input.ZSXQDynamicIntervalSec, ZSXQCommentIntervalSec: *input.ZSXQCommentIntervalSec,
		ZSXQCommentsEnabled: *input.ZSXQCommentsEnabled, ZSXQRequestRate: *input.ZSXQRequestRate, ZSXQRequestConcurrency: *input.ZSXQRequestConcurrency,
		ZSXQRiskPauseSec: *input.ZSXQRiskPauseSec, ZSXQAssetMaxFileMiB: *input.ZSXQAssetMaxFileMiB, ZSXQAssetTotalBudgetGiB: *input.ZSXQAssetTotalBudgetGiB,
		LogLevel: *input.LogLevel, AuditLogRetentionDays: *input.AuditLogRetentionDays,
		DeliveryConcurrency: *input.DeliveryConcurrency,
		BacklogAlertCount:   *input.BacklogAlertCount, BacklogAlertAgeSec: *input.BacklogAlertAgeSec,
		AIAutoProcessingEnabled: *input.AIAutoProcessingEnabled,
	}
	copy(settings.DeliveryRetryDelaysSec[:], *input.DeliveryRetryDelaysSec)
	return settings, nil
}

func (s *Server) updateSettingsAPI(w http.ResponseWriter, r *http.Request) {
	var request runtimeSettingsRequest
	if !decodeAPIRequest(w, r, &request) {
		return
	}
	input, err := request.settings()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := input.Validate(); err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(err))
		return
	}
	before := s.settings.Settings()
	err = s.settings.UpdateSettings(input)
	if err == nil {
		setAuditDetails(r, map[string]any{"before": before, "after": s.settings.Settings()})
	}
	s.writeAPIResult(w, http.StatusOK, s.settings.Settings(), err)
}

func (s *Server) queryAuditLogsAPI(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	input := state.AuditQuery{
		Action: strings.TrimSpace(query.Get("action")), Outcome: strings.TrimSpace(query.Get("outcome")),
		ResourceType: strings.TrimSpace(query.Get("resource_type")), Q: query.Get("q"),
	}
	if input.Outcome != "" && input.Outcome != state.AuditSuccess && input.Outcome != state.AuditFailure && input.Outcome != state.AuditDenied {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "outcome must be success, failure, or denied")
		return
	}
	var err error
	if value := query.Get("from"); value != "" {
		input.From, err = time.Parse(time.RFC3339, value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "from must be RFC3339")
			return
		}
	}
	if value := query.Get("to"); value != "" {
		input.To, err = time.Parse(time.RFC3339, value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "to must be RFC3339")
			return
		}
	}
	if !input.From.IsZero() && !input.To.IsZero() && !input.From.Before(input.To) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "from must be earlier than to")
		return
	}
	if value := query.Get("limit"); value != "" {
		input.Limit, err = strconv.Atoi(value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer")
			return
		}
	}
	limit, ok := normalizeRequestedLimit(w, input.Limit, 50)
	if !ok {
		return
	}
	cursor, ok := parseAfterCursor(w, r)
	if !ok {
		return
	}
	input.Limit = limit + 1
	if cursor != nil {
		input.AfterOccurredAt = time.UnixMilli(cursor.Sort)
		input.AfterID, err = strconv.ParseInt(cursor.Key, 10, 64)
		if err != nil || input.AfterID <= 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "after cursor is invalid")
			return
		}
	}
	items, _, err := s.store.WithContext(r.Context()).QueryAuditLogs(input)
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	next := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next = encodeListCursor(last.OccurredAt.UnixMilli(), strconv.FormatInt(last.ID, 10))
	}
	writeJSON(w, http.StatusOK, cursorPageResponse{Items: items, Page: cursorPage{NextCursor: next, HasMore: hasMore}})
}

func channelAuditDetails(before *model.Channel, after model.Channel) map[string]any {
	details := make(map[string]any)
	if before != nil {
		details["before"] = map[string]any{"name": before.Name, "type": before.Type, "enabled": before.Enabled}
	}
	if after.ID != "" {
		details["after"] = map[string]any{"name": after.Name, "type": after.Type, "enabled": after.Enabled}
	}
	changed := make(map[string]struct{})
	if before != nil {
		for key, value := range before.Settings {
			if after.Settings[key] != value {
				changed[key] = struct{}{}
			}
		}
	}
	for key, value := range after.Settings {
		if before == nil || before.Settings[key] != value {
			changed[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(changed))
	for key := range changed {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if len(keys) > 0 {
		details["changed_setting_keys"] = keys
	}
	return details
}

func parsePageLimit(w http.ResponseWriter, r *http.Request, defaultLimit int) (int, bool) {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return defaultLimit, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer")
		return 0, false
	}
	return normalizeRequestedLimit(w, limit, defaultLimit)
}

func normalizeRequestedLimit(w http.ResponseWriter, limit, defaultLimit int) (int, bool) {
	if limit == 0 {
		return defaultLimit, true
	}
	if limit < 1 || limit > 100 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
		return 0, false
	}
	return limit, true
}

func parseAfterCursor(w http.ResponseWriter, r *http.Request) (*listCursor, bool) {
	value := strings.TrimSpace(r.URL.Query().Get("after"))
	if value == "" {
		return nil, true
	}
	if len(value) > 1024 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "after cursor is invalid")
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "after cursor is invalid")
		return nil, false
	}
	var cursor listCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Version != 1 || strings.TrimSpace(cursor.Key) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "after cursor is invalid")
		return nil, false
	}
	return &cursor, true
}

func encodeListCursor(sortValue int64, key string) string {
	raw, _ := json.Marshal(listCursor{Version: 1, Sort: sortValue, Key: key})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (s *Server) writeAPIResult(w http.ResponseWriter, successStatus int, value any, err error) {
	if err == nil {
		writeJSON(w, successStatus, value)
		return
	}
	apiErr := apiError(err)
	if apiErr.Code == "internal" && s.logger != nil {
		s.logger.Error("admin API request failed", "event", "http.handler.failed", "error", err)
	}
	writeAPIError(w, httpStatusForAPIError(apiErr.Code), apiErr.Code, apiErr.Message)
}

func decodeAPIRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := decodeJSON(r, dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	return true
}

func httpStatusForAPIError(code string) int {
	switch code {
	case "invalid_request", "validation_failed":
		return http.StatusBadRequest
	case "not_found":
		return http.StatusNotFound
	case "conflict":
		return http.StatusConflict
	case "upstream_failure":
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func withAPITimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 20*time.Second)
}
