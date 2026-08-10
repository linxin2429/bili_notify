package web

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	"github.com/linxin2429/bili_notify/zsxq"
)

func (s *Server) registerPlatformAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v3/accounts", s.requireSession(false, s.accountsV3))
	mux.HandleFunc("GET /api/v3/accounts/bilibili/qr", s.requireSession(false, s.biliLoginAPI))
	mux.HandleFunc("POST /api/v3/accounts/bilibili/qr", s.audit("bilibili.login.start", "platform_account", "", s.requireSession(true, s.startBiliLoginAPI)))
	mux.HandleFunc("DELETE /api/v3/accounts/bilibili/qr/{id}", s.audit("bilibili.login.cancel", "platform_account", "id", s.requireSession(true, s.cancelBiliLoginAPI)))
	mux.HandleFunc("DELETE /api/v3/accounts/bilibili/session", s.audit("bilibili.logout", "platform_account", "", s.requireSession(true, s.deleteBilibiliSessionV3)))
	mux.HandleFunc("POST /api/v3/accounts/zsxq/sms-code", s.audit("zsxq.sms.send", "platform_account", "", s.requireSession(true, s.zsxqSMSCodeV3)))
	mux.HandleFunc("POST /api/v3/accounts/zsxq/session", s.audit("zsxq.login", "platform_account", "", s.requireSession(true, s.zsxqSessionV3)))
	mux.HandleFunc("DELETE /api/v3/accounts/zsxq/session", s.audit("zsxq.logout", "platform_account", "", s.requireSession(true, s.deleteZSXQSessionV3)))
	mux.HandleFunc("POST /api/v3/accounts/zsxq/sync-sources", s.audit("zsxq.sources.sync", "source", "", s.requireSession(true, s.syncZSXQSourcesV3)))

	mux.HandleFunc("GET /api/v3/sources", s.requireSession(false, s.sourcesV3))
	mux.HandleFunc("POST /api/v3/sources", s.audit("source.create", "source", "", s.requireSession(true, s.createSourceV3)))
	mux.HandleFunc("PUT /api/v3/sources/{id}", s.audit("source.update", "source", "id", s.requireSession(true, s.updateSourceV3)))
	mux.HandleFunc("DELETE /api/v3/sources/{id}", s.audit("source.delete", "source", "id", s.requireSession(true, s.deleteSourceV3)))

	mux.HandleFunc("GET /api/v3/contents", s.requireSession(false, s.contentsV3))
	mux.HandleFunc("GET /api/v3/contents/{id}", s.requireSession(false, s.contentV3))
	mux.HandleFunc("GET /api/v3/contents/{id}/comments", s.requireSession(false, s.commentTreeV3))
	mux.HandleFunc("GET /api/v3/contents/{id}/attachments/{attachment_id}", s.requireSession(false, s.attachmentV3))
}

func (s *Server) accountsV3(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.WithContext(r.Context()).ListPlatformAccounts()
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) deleteBilibiliSessionV3(w http.ResponseWriter, _ *http.Request) {
	if err := s.engine.ClearBilibiliSession(); err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) zsxqSMSCodeV3(w http.ResponseWriter, r *http.Request) {
	if s.zsxqLogin == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "integration_unavailable", "Knowledge Planet integration is unavailable")
		return
	}
	var input zsxq.SMSCodeRequest
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	if err := input.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	transaction, err := s.zsxqLogin.SendCode(ctx, input)
	if err != nil {
		writeZSXQAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, transaction)
}

func (s *Server) zsxqSessionV3(w http.ResponseWriter, r *http.Request) {
	if s.zsxqLogin == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "integration_unavailable", "Knowledge Planet integration is unavailable")
		return
	}
	var input struct {
		TransactionID string `json:"transaction_id"`
		Code          string `json:"code"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.TransactionID) == "" || strings.TrimSpace(input.Code) == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "transaction_id and code are required")
		return
	}
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	account, err := s.zsxqLogin.SubmitCode(ctx, input.TransactionID, input.Code)
	if err != nil {
		writeZSXQAPIError(w, err)
		return
	}
	s.events.Publish(service.TopicAccounts | service.TopicSources)
	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) deleteZSXQSessionV3(w http.ResponseWriter, r *http.Request) {
	if s.zsxqLogin != nil {
		s.zsxqLogin.ClearSession()
	}
	err := s.store.WithContext(r.Context()).DeletePlatformAccount(model.PlatformZSXQ)
	if errors.Is(err, state.ErrNotFound) {
		err = nil
	}
	if err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	s.events.Publish(service.TopicAccounts)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncZSXQSourcesV3(w http.ResponseWriter, r *http.Request) {
	if s.zsxqLogin == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "integration_unavailable", "Knowledge Planet integration is unavailable")
		return
	}
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	sources, err := s.zsxqLogin.SyncSources(ctx)
	if err != nil {
		writeZSXQAPIError(w, err)
		return
	}
	s.events.Publish(service.TopicSources)
	writeJSON(w, http.StatusOK, sources)
}

func writeZSXQAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, zsxq.ErrInvalidPhone):
		writeAPIError(w, http.StatusBadRequest, "invalid_phone", "phone number is invalid")
	case errors.Is(err, zsxq.ErrPhoneUnbound):
		writeAPIError(w, http.StatusConflict, "phone_unbound", "phone number is not bound to a Knowledge Planet account")
	case errors.Is(err, zsxq.ErrSMSCooldown), errors.Is(err, zsxq.ErrAttemptsExceeded), errors.Is(err, zsxq.ErrRateLimited), errors.Is(err, zsxq.ErrRiskControl):
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, zsxq.ErrLoginExpired):
		writeAPIError(w, http.StatusGone, "login_expired", err.Error())
	case errors.Is(err, zsxq.ErrLoginNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, zsxq.ErrAuthentication):
		writeAPIError(w, http.StatusUnauthorized, "authentication_failed", "Knowledge Planet authentication failed")
	case errors.Is(err, zsxq.ErrSchemaDrift), errors.Is(err, zsxq.ErrUpstream):
		writeAPIError(w, http.StatusBadGateway, "upstream_failure", "Knowledge Planet upstream request failed")
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func (s *Server) sourcesV3(w http.ResponseWriter, r *http.Request) {
	platform := model.Platform(strings.TrimSpace(r.URL.Query().Get("platform")))
	if platform != "" {
		if err := platform.Validate(); err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
	}
	sources, err := s.store.WithContext(r.Context()).ListSources(platform)
	s.writeAPIResult(w, http.StatusOK, sources, err)
}

func (s *Server) createSourceV3(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Platform   model.Platform `json:"platform"`
		ExternalID string         `json:"external_id"`
		Name       string         `json:"name"`
		Note       string         `json:"note"`
		Enabled    bool           `json:"enabled"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	if input.Platform != model.PlatformBilibili {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "only Bilibili UP sources may be created manually")
		return
	}
	source := model.Source{ID: model.SourceID(input.Platform, input.ExternalID), Platform: input.Platform, Type: model.SourceBilibiliUP,
		ExternalID: input.ExternalID, Name: input.Name, Note: input.Note, Enabled: input.Enabled, BaselineState: model.BaselinePending}
	if err := source.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	if _, err := s.store.WithContext(r.Context()).Source(source.ID); err == nil {
		s.writeAPIResult(w, http.StatusCreated, nil, conflictFailure(errors.New("source already exists")))
		return
	} else if !errors.Is(err, state.ErrNotFound) {
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
	if err := s.store.WithContext(r.Context()).PutSource(source); err != nil {
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
	if err := s.store.WithContext(r.Context()).PutUP(model.UP{UID: source.ExternalID, Name: source.Name, Enabled: source.Enabled}); err != nil {
		_ = s.store.DeleteSource(source.ID)
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
	s.engine.NotifyUPChanged()
	setAuditResourceID(r, source.ID)
	setAuditDetails(r, map[string]any{"platform": source.Platform, "external_id": source.ExternalID, "enabled": source.Enabled})
	s.events.Publish(service.TopicStatus | service.TopicUPs)
	s.events.Publish(service.TopicSources)
	s.writeAPIResult(w, http.StatusCreated, source, nil)
}

func (s *Server) updateSourceV3(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name    string `json:"name"`
		Note    string `json:"note"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	source, err := s.store.WithContext(r.Context()).Source(r.PathValue("id"))
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	source.Note, source.Enabled = input.Note, input.Enabled
	if input.Name != "" {
		source.Name = input.Name
	}
	if source.Enabled && source.BaselineState == model.BaselineFailed {
		source.BaselineState = model.BaselineRunning
	}
	if err := s.store.WithContext(r.Context()).PutSource(source); err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	if source.Platform == model.PlatformBilibili {
		up, upErr := s.store.WithContext(r.Context()).UP(source.ExternalID)
		if errors.Is(upErr, state.ErrNotFound) {
			up = model.UP{UID: source.ExternalID}
		} else if upErr != nil {
			s.writeAPIResult(w, http.StatusOK, nil, upErr)
			return
		}
		up.Name, up.Enabled = source.Name, source.Enabled
		if err := s.store.WithContext(r.Context()).PutUP(up); err != nil {
			s.writeAPIResult(w, http.StatusOK, nil, err)
			return
		}
		s.engine.NotifyUPChanged()
	}
	setAuditDetails(r, map[string]any{"name": source.Name, "note": source.Note, "enabled": source.Enabled})
	s.writeAPIResult(w, http.StatusOK, source, nil)
	s.events.Publish(service.TopicSources | service.TopicBackfills)
}

func (s *Server) deleteSourceV3(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	source, err := s.store.WithContext(r.Context()).Source(id)
	if err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	if source.Platform == model.PlatformBilibili {
		err = s.store.WithContext(r.Context()).DeleteUP(source.ExternalID)
	} else {
		err = s.store.WithContext(r.Context()).DeleteSource(id)
	}
	if err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	if s.dataDir != "" {
		_ = media.RemoveSource(s.dataDir, source.Platform, source.ID)
	}
	s.events.Publish(service.TopicSources | service.TopicContents | service.TopicBackfills)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) contentsV3(w http.ResponseWriter, r *http.Request) {
	limit, ok := parsePageLimit(w, r, 20)
	if !ok {
		return
	}
	cursor, ok := parseAfterCursor(w, r)
	if !ok {
		return
	}
	platform := model.Platform(r.URL.Query().Get("platform"))
	if platform != "" {
		if err := platform.Validate(); err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
	}
	query := state.PlatformContentQuery{Platform: platform, SourceID: r.URL.Query().Get("source_id"), Keyword: r.URL.Query().Get("q"), Limit: limit + 1}
	if raw := r.URL.Query().Get("from"); raw != "" {
		var err error
		query.From, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "from must be RFC3339")
			return
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		var err error
		query.To, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "to must be RFC3339")
			return
		}
	}
	if !query.From.IsZero() && !query.To.IsZero() && !query.From.Before(query.To) {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "from must be before to")
		return
	}
	if cursor != nil {
		query.AfterAt, query.AfterID = time.Unix(cursor.Sort, 0), cursor.Key
	}
	contents, err := s.store.WithContext(r.Context()).QueryContents(query)
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	hasMore := len(contents) > limit
	if hasMore {
		contents = contents[:limit]
	}
	next := ""
	if hasMore && len(contents) > 0 {
		last := contents[len(contents)-1]
		next = encodeListCursor(last.PublishedAt.Unix(), last.ID)
	}
	writeJSON(w, http.StatusOK, cursorPageResponse{Items: contents, Page: cursorPage{HasMore: hasMore, NextCursor: next}})
}

func (s *Server) contentV3(w http.ResponseWriter, r *http.Request) {
	content, attachments, err := s.store.WithContext(r.Context()).Content(r.PathValue("id"))
	s.writeAPIResult(w, http.StatusOK, map[string]any{"content": content, "attachments": attachments}, err)
}

func (s *Server) commentTreeV3(w http.ResponseWriter, r *http.Request) {
	tree, incomplete, err := s.store.WithContext(r.Context()).CommentTree(r.PathValue("id"))
	s.writeAPIResult(w, http.StatusOK, map[string]any{"children": tree, "incomplete": incomplete}, err)
}

func (s *Server) attachmentV3(w http.ResponseWriter, r *http.Request) {
	attachment, err := s.store.WithContext(r.Context()).Attachment(r.PathValue("id"), r.PathValue("attachment_id"), false)
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	if attachment.LocalPath == "" || s.dataDir == "" {
		writeAPIError(w, http.StatusNotFound, "not_found", "local attachment is unavailable")
		return
	}
	abs, err := media.Resolve(s.dataDir, attachment.LocalPath)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "local attachment path is invalid")
		return
	}
	file, err := os.Open(abs)
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	name := attachment.FileName
	if name == "" {
		name = filepath.Base(abs)
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if attachment.MIME != "" {
		w.Header().Set("Content-Type", attachment.MIME)
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func parseOptionalInt(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.Atoi(raw)
}
