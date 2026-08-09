package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
)

func (s *Server) registerAdminAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/dashboard", s.requireSession(false, s.dashboardAPI))
	mux.HandleFunc("POST /api/v1/ups", s.audit("up.create", "up", "", s.requireSession(true, s.createUPAPI)))
	mux.HandleFunc("PUT /api/v1/ups/{uid}", s.audit("up.update", "up", "uid", s.requireSession(true, s.updateUPAPI)))
	mux.HandleFunc("DELETE /api/v1/ups/{uid}", s.audit("up.delete", "up", "uid", s.requireSession(true, s.deleteUPAPI)))
	mux.HandleFunc("POST /api/v1/channels", s.audit("channel.create", "channel", "", s.requireSession(true, s.createChannelAPI)))
	mux.HandleFunc("PUT /api/v1/channels/{id}", s.audit("channel.update", "channel", "id", s.requireSession(true, s.updateChannelAPI)))
	mux.HandleFunc("DELETE /api/v1/channels/{id}", s.audit("channel.delete", "channel", "id", s.requireSession(true, s.deleteChannelAPI)))
	mux.HandleFunc("POST /api/v1/channels/{id}/test", s.audit("channel.test", "channel", "id", s.requireSession(true, s.testChannelAPI)))
	mux.HandleFunc("POST /api/v1/deliveries/{id}/retry", s.audit("delivery.retry", "delivery", "id", s.requireSession(true, s.retryDeliveryAPI)))
	mux.HandleFunc("POST /api/v1/bilibili-login", s.audit("bilibili.login.start", "bilibili_login", "", s.requireSession(true, s.startBiliLoginAPI)))
	mux.HandleFunc("DELETE /api/v1/bilibili-login/{id}", s.audit("bilibili.login.cancel", "bilibili_login", "id", s.requireSession(true, s.cancelBiliLoginAPI)))
	mux.HandleFunc("POST /api/v1/channels/{id}/microsoft-login", s.audit("microsoft.login.start", "channel", "id", s.requireSession(true, s.startMicrosoftLoginAPI)))
	mux.HandleFunc("DELETE /api/v1/channels/{id}/microsoft-login", s.audit("microsoft.login.cancel", "channel", "id", s.requireSession(true, s.cancelMicrosoftLoginAPI)))
	mux.HandleFunc("PUT /api/v1/settings", s.audit("settings.update", "settings", "", s.requireSession(true, s.updateSettingsAPI)))
	mux.HandleFunc("GET /api/v1/audit-logs", s.requireSession(false, s.queryAuditLogsAPI))
	mux.HandleFunc("GET /api/v1/dynamics", s.requireSession(false, s.queryDynamicsAPI))
	mux.HandleFunc("GET /api/v1/dynamics/{id}", s.requireSession(false, s.getDynamicAPI))
	mux.HandleFunc("GET /api/v1/dynamics/{id}/media/{index}", s.requireSession(false, s.getDynamicMediaAPI))
	mux.HandleFunc("GET /api/v1/comments", s.requireSession(false, s.queryCommentsAPI))
	mux.HandleFunc("GET /api/v1/comments/{rpid}", s.requireSession(false, s.getCommentAPI))
}

func (s *Server) dashboardAPI(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.snapshot()
	s.writeAPIResult(w, http.StatusOK, snapshot, err)
}

func (s *Server) createUPAPI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		UID     string `json:"uid"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	up := model.UP{
		UID: input.UID, Name: input.Name, Enabled: input.Enabled,
		FollowState: model.FollowUnknown, CollectionRoute: model.CollectionRouteSpace,
	}
	if err := up.Validate(); err != nil {
		s.writeAPIResult(w, http.StatusCreated, nil, validationFailure(err))
		return
	}
	if _, err := s.store.WithContext(r.Context()).UP(up.UID); err == nil {
		s.writeAPIResult(w, http.StatusCreated, nil, conflictFailure(errors.New("UP already exists")))
		return
	} else if !errors.Is(err, state.ErrNotFound) {
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
	if err := s.store.WithContext(r.Context()).PutUP(up); err != nil {
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
	setAuditResourceID(r, up.UID)
	setAuditDetails(r, map[string]any{"name": up.Name, "enabled": up.Enabled})
	s.engine.NotifyUPChanged()
	s.events.Publish(service.TopicStatus | service.TopicUPs)
	s.writeAPIResult(w, http.StatusCreated, up, nil)
}

func (s *Server) updateUPAPI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	up, err := s.store.WithContext(r.Context()).UP(r.PathValue("uid"))
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	beforeName, beforeEnabled := up.Name, up.Enabled
	up.Name, up.Enabled = input.Name, input.Enabled
	if err := up.Validate(); err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(err))
		return
	}
	if err := s.store.WithContext(r.Context()).PutUP(up); err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	setAuditDetails(r, map[string]any{
		"before": map[string]any{"name": beforeName, "enabled": beforeEnabled},
		"after":  map[string]any{"name": up.Name, "enabled": up.Enabled},
	})
	s.engine.NotifyUPChanged()
	s.events.Publish(service.TopicStatus | service.TopicUPs)
	s.writeAPIResult(w, http.StatusOK, up, nil)
}

func (s *Server) deleteUPAPI(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if up, err := s.store.WithContext(r.Context()).UP(uid); err == nil {
		setAuditDetails(r, map[string]any{"name": up.Name, "enabled": up.Enabled})
	}
	if err := s.store.WithContext(r.Context()).DeleteUP(uid); err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	if s.dataDir != "" {
		if err := media.RemoveUP(s.dataDir, uid); err != nil {
			s.logger.WarnContext(r.Context(), "removing media for deleted UP failed", "event", "media.remove_up.failed", "up_uid", uid, "error", err)
		}
	}
	s.engine.NotifyUPChanged()
	s.events.Publish(service.TopicStatus | service.TopicUPs)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createChannelAPI(w http.ResponseWriter, r *http.Request) {
	var input channelInput
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	channel, err := s.saveChannel(input, false)
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
	input.ID = r.PathValue("id")
	var before *model.Channel
	if current, currentErr := s.store.WithContext(r.Context()).Channel(input.ID); currentErr == nil {
		before = &current
	}
	channel, err := s.saveChannel(input, true)
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
	PollIntervalSec         *int     `json:"poll_interval_sec"`
	RequestRate             *float64 `json:"request_rate"`
	RequestConcurrency      *int     `json:"request_concurrency"`
	CommentEnabled          *bool    `json:"comment_enabled"`
	CommentTrackN           *int     `json:"comment_track_n"`
	CommentRootPages        *int     `json:"comment_root_pages"`
	CommentReplyPages       *int     `json:"comment_reply_pages"`
	CommentBatchIntervalSec *int     `json:"comment_batch_interval_sec"`
	LogLevel                *string  `json:"log_level"`
	AuditLogRetentionDays   *int     `json:"audit_log_retention_days"`
	RelationRefreshSec      *int     `json:"relation_refresh_interval_sec"`
	SpaceReconcileSec       *int     `json:"space_reconcile_interval_sec"`
	MaxDynamicPages         *int     `json:"max_dynamic_pages"`
	RiskPauseSec            *int     `json:"risk_pause_sec"`
	DeliveryConcurrency     *int     `json:"delivery_concurrency"`
	BacklogAlertCount       *int     `json:"backlog_alert_count"`
	BacklogAlertAgeSec      *int     `json:"backlog_alert_age_sec"`
	DeliveryRetryDelaysSec  *[]int   `json:"delivery_retry_delays_sec"`
}

func (input runtimeSettingsRequest) settings() (model.RuntimeSettings, error) {
	missing := make([]string, 0)
	for name, present := range map[string]bool{
		"poll_interval_sec": input.PollIntervalSec != nil, "request_rate": input.RequestRate != nil,
		"request_concurrency": input.RequestConcurrency != nil, "comment_enabled": input.CommentEnabled != nil,
		"comment_track_n": input.CommentTrackN != nil, "comment_root_pages": input.CommentRootPages != nil,
		"comment_reply_pages": input.CommentReplyPages != nil, "comment_batch_interval_sec": input.CommentBatchIntervalSec != nil,
		"log_level": input.LogLevel != nil, "audit_log_retention_days": input.AuditLogRetentionDays != nil,
		"relation_refresh_interval_sec": input.RelationRefreshSec != nil,
		"space_reconcile_interval_sec":  input.SpaceReconcileSec != nil, "max_dynamic_pages": input.MaxDynamicPages != nil,
		"risk_pause_sec": input.RiskPauseSec != nil, "delivery_concurrency": input.DeliveryConcurrency != nil,
		"backlog_alert_count": input.BacklogAlertCount != nil, "backlog_alert_age_sec": input.BacklogAlertAgeSec != nil,
		"delivery_retry_delays_sec": input.DeliveryRetryDelaysSec != nil,
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
		PollIntervalSec: *input.PollIntervalSec, RequestRate: *input.RequestRate, RequestConcurrency: *input.RequestConcurrency,
		CommentEnabled: *input.CommentEnabled, CommentTrackN: *input.CommentTrackN, CommentRootPages: *input.CommentRootPages,
		CommentReplyPages: *input.CommentReplyPages, CommentBatchIntervalSec: *input.CommentBatchIntervalSec,
		LogLevel: *input.LogLevel, AuditLogRetentionDays: *input.AuditLogRetentionDays,
		RelationRefreshSec: *input.RelationRefreshSec, SpaceReconcileSec: *input.SpaceReconcileSec, MaxDynamicPages: *input.MaxDynamicPages,
		RiskPauseSec: *input.RiskPauseSec, DeliveryConcurrency: *input.DeliveryConcurrency,
		BacklogAlertCount: *input.BacklogAlertCount, BacklogAlertAgeSec: *input.BacklogAlertAgeSec,
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
	if value := query.Get("offset"); value != "" {
		input.Offset, err = strconv.Atoi(value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "offset must be an integer")
			return
		}
	}
	items, total, err := s.store.WithContext(r.Context()).QueryAuditLogs(input)
	limit, offset := normalizeAuditPage(input.Limit, input.Offset)
	s.writeAPIResult(w, http.StatusOK, contentPage{Items: items, Total: total, Limit: limit, Offset: offset}, err)
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

func normalizeAuditPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	return min(limit, 100), max(offset, 0)
}

func (s *Server) queryDynamicsAPI(w http.ResponseWriter, r *http.Request) {
	s.queryContentAPI(w, r, true)
}

func (s *Server) queryCommentsAPI(w http.ResponseWriter, r *http.Request) {
	s.queryContentAPI(w, r, false)
}

func (s *Server) queryContentAPI(w http.ResponseWriter, r *http.Request, dynamics bool) {
	query := r.URL.Query()
	input := contentQueryInput{UID: query.Get("uid"), Q: query.Get("q"), From: query.Get("from"), To: query.Get("to")}
	var err error
	if value := query.Get("limit"); value != "" {
		input.Limit, err = strconv.Atoi(value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer")
			return
		}
	}
	if value := query.Get("offset"); value != "" {
		input.Offset, err = strconv.Atoi(value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "offset must be an integer")
			return
		}
	}
	q, err := parseContentQuery(input)
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(err))
		return
	}
	limit, offset := normalizeQueryPage(q.Limit, q.Offset)
	if dynamics {
		items, total, err := s.store.WithContext(r.Context()).QueryDynamics(q)
		views := make([]dynamicHistoryView, 0, len(items))
		for _, item := range items {
			views = append(views, toDynamicHistoryView(item))
		}
		s.writeAPIResult(w, http.StatusOK, contentPage{Items: views, Total: total, Limit: limit, Offset: offset}, err)
		return
	}
	items, total, err := s.store.WithContext(r.Context()).QueryComments(q)
	views := make([]commentHistoryView, 0, len(items))
	for _, item := range items {
		views = append(views, toCommentHistoryView(item))
	}
	s.writeAPIResult(w, http.StatusOK, contentPage{Items: views, Total: total, Limit: limit, Offset: offset}, err)
}

func (s *Server) getDynamicAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(errors.New("id is required")))
		return
	}
	dynamic, err := s.store.WithContext(r.Context()).GetDynamic(id)
	s.writeAPIResult(w, http.StatusOK, dynamic, err)
}

func (s *Server) getDynamicMediaAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "id is required")
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "media index must be a non-negative integer")
		return
	}
	dynamic, err := s.store.WithContext(r.Context()).GetDynamic(id)
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	if index >= len(dynamic.Media) {
		writeAPIError(w, http.StatusNotFound, "not_found", "media index not found")
		return
	}
	item := dynamic.Media[index]
	if item.LocalPath == "" || s.dataDir == "" {
		writeAPIError(w, http.StatusNotFound, "not_found", "local media is not available")
		return
	}
	abs, err := media.Resolve(s.dataDir, item.LocalPath)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "local media path is invalid")
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.metrics.RecordMediaMissing(r.Context())
			writeAPIError(w, http.StatusNotFound, "not_found", "local media file is missing")
			return
		}
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	contentType := item.ContentType
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) getCommentAPI(w http.ResponseWriter, r *http.Request) {
	rpid := strings.TrimSpace(r.PathValue("rpid"))
	if rpid == "" {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(errors.New("rpid is required")))
		return
	}
	note, err := s.store.WithContext(r.Context()).GetComment(rpid)
	s.writeAPIResult(w, http.StatusOK, note, err)
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
