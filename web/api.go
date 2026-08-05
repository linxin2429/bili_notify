package web

import (
	"context"
	"errors"
	"net/http"
	"os"
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
	mux.HandleFunc("POST /api/v1/ups", s.requireSession(true, s.createUPAPI))
	mux.HandleFunc("PUT /api/v1/ups/{uid}", s.requireSession(true, s.updateUPAPI))
	mux.HandleFunc("DELETE /api/v1/ups/{uid}", s.requireSession(true, s.deleteUPAPI))
	mux.HandleFunc("POST /api/v1/channels", s.requireSession(true, s.createChannelAPI))
	mux.HandleFunc("PUT /api/v1/channels/{id}", s.requireSession(true, s.updateChannelAPI))
	mux.HandleFunc("DELETE /api/v1/channels/{id}", s.requireSession(true, s.deleteChannelAPI))
	mux.HandleFunc("POST /api/v1/channels/{id}/test", s.requireSession(true, s.testChannelAPI))
	mux.HandleFunc("POST /api/v1/deliveries/{id}/retry", s.requireSession(true, s.retryDeliveryAPI))
	mux.HandleFunc("POST /api/v1/bilibili-login", s.requireSession(true, s.startBiliLoginAPI))
	mux.HandleFunc("DELETE /api/v1/bilibili-login/{id}", s.requireSession(true, s.cancelBiliLoginAPI))
	mux.HandleFunc("POST /api/v1/channels/{id}/microsoft-login", s.requireSession(true, s.startMicrosoftLoginAPI))
	mux.HandleFunc("DELETE /api/v1/channels/{id}/microsoft-login", s.requireSession(true, s.cancelMicrosoftLoginAPI))
	mux.HandleFunc("PUT /api/v1/settings", s.requireSession(true, s.updateSettingsAPI))
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
	up := model.UP{UID: input.UID, Name: input.Name, Enabled: input.Enabled}
	if err := up.Validate(); err != nil {
		s.writeAPIResult(w, http.StatusCreated, nil, validationFailure(err))
		return
	}
	if _, err := s.store.UP(up.UID); err == nil {
		s.writeAPIResult(w, http.StatusCreated, nil, conflictFailure(errors.New("UP already exists")))
		return
	} else if !errors.Is(err, state.ErrNotFound) {
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
	if err := s.store.PutUP(up); err != nil {
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
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
	up, err := s.store.UP(r.PathValue("uid"))
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	up.Name, up.Enabled = input.Name, input.Enabled
	if err := up.Validate(); err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(err))
		return
	}
	if err := s.store.PutUP(up); err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	s.events.Publish(service.TopicStatus | service.TopicUPs)
	s.writeAPIResult(w, http.StatusOK, up, nil)
}

func (s *Server) deleteUPAPI(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if err := s.store.DeleteUP(uid); err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	if s.dataDir != "" {
		if err := media.RemoveUP(s.dataDir, uid); err != nil {
			s.logger.Warn("removing media for deleted UP failed", "uid", uid, "err", err)
		}
	}
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
	channel, err := s.saveChannel(input, true)
	if err == nil {
		s.events.Publish(service.TopicStatus | service.TopicChannels | service.TopicDeliveries | service.TopicMicrosoftLogin)
	}
	s.writeAPIResult(w, http.StatusOK, toChannelView(channel), err)
}

func (s *Server) deleteChannelAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteChannel(id); err != nil {
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
		writeAPIError(w, http.StatusBadGateway, "upstream_failure", err.Error())
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
	if err := s.store.RetryDelivery(id, time.Now()); err != nil {
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

func (s *Server) updateSettingsAPI(w http.ResponseWriter, r *http.Request) {
	var input model.RuntimeSettings
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	if err := input.Validate(); err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, validationFailure(err))
		return
	}
	err := s.engine.UpdateSettings(input)
	s.writeAPIResult(w, http.StatusOK, s.engine.Settings(), err)
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
		items, total, err := s.store.QueryDynamics(q)
		views := make([]dynamicHistoryView, 0, len(items))
		for _, item := range items {
			views = append(views, toDynamicHistoryView(item))
		}
		s.writeAPIResult(w, http.StatusOK, contentPage{Items: views, Total: total, Limit: limit, Offset: offset}, err)
		return
	}
	items, total, err := s.store.QueryComments(q)
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
	dynamic, err := s.store.GetDynamic(id)
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
	dynamic, err := s.store.GetDynamic(id)
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
	note, err := s.store.GetComment(rpid)
	s.writeAPIResult(w, http.StatusOK, note, err)
}

func (s *Server) writeAPIResult(w http.ResponseWriter, successStatus int, value any, err error) {
	if err == nil {
		writeJSON(w, successStatus, value)
		return
	}
	apiErr := apiError(err)
	if apiErr.Code == "internal" && s.logger != nil {
		s.logger.Error("admin API request failed", "err", err)
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
