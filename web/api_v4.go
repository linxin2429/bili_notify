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
	mux.HandleFunc("GET /api/v4/accounts", s.requireSession(false, s.accountsV4))
	mux.HandleFunc("GET /api/v4/accounts/bilibili/qr", s.requireSession(false, s.biliLoginAPI))
	mux.HandleFunc("POST /api/v4/accounts/bilibili/qr", s.audit("bilibili.login.start", "platform_account", "", s.requireSession(true, s.startBiliLoginAPI)))
	mux.HandleFunc("DELETE /api/v4/accounts/bilibili/qr/{id}", s.audit("bilibili.login.cancel", "platform_account", "id", s.requireSession(true, s.cancelBiliLoginAPI)))
	mux.HandleFunc("DELETE /api/v4/accounts/bilibili/session", s.audit("bilibili.logout", "platform_account", "", s.requireSession(true, s.deleteBilibiliSessionV4)))
	mux.HandleFunc("PUT /api/v4/accounts/zsxq/credential", s.audit("zsxq.credential.update", "platform_account", "", s.requireSession(true, s.updateZSXQCredentialV4)))
	mux.HandleFunc("DELETE /api/v4/accounts/zsxq/credential", s.audit("zsxq.credential.delete", "platform_account", "", s.requireSession(true, s.deleteZSXQCredentialV4)))
	mux.HandleFunc("GET /api/v4/accounts/zsxq/groups", s.requireSession(false, s.zsxqGroupsV4))

	mux.HandleFunc("GET /api/v4/sources", s.requireSession(false, s.sourcesV4))
	mux.HandleFunc("POST /api/v4/sources/bilibili", s.audit("source.create", "source", "", s.requireSession(true, s.createBilibiliSourceV4)))
	mux.HandleFunc("POST /api/v4/sources/zsxq", s.audit("source.create", "source", "", s.requireSession(true, s.createZSXQSourceV4)))
	mux.HandleFunc("PUT /api/v4/sources/{id}", s.audit("source.update", "source", "id", s.requireSession(true, s.updateSourceV4)))
	mux.HandleFunc("DELETE /api/v4/sources/{id}", s.audit("source.delete", "source", "id", s.requireSession(true, s.deleteSourceV4)))

	mux.HandleFunc("GET /api/v4/contents", s.requireSession(false, s.contentsV4))
	mux.HandleFunc("GET /api/v4/contents/{id}", s.requireSession(false, s.contentV4))
	mux.HandleFunc("GET /api/v4/contents/{id}/comments", s.requireSession(false, s.commentTreeV4))
	mux.HandleFunc("GET /api/v4/contents/{id}/attachments/{attachment_id}", s.requireSession(false, s.attachmentV4))
}

func (s *Server) accountsV4(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.WithContext(r.Context()).ListPlatformAccounts()
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) deleteBilibiliSessionV4(w http.ResponseWriter, _ *http.Request) {
	if err := s.engine.ClearBilibiliSession(); err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateZSXQCredentialV4(w http.ResponseWriter, r *http.Request) {
	if s.zsxqAccounts == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "integration_unavailable", "Knowledge Planet integration is unavailable")
		return
	}
	var input struct {
		APIKey string `json:"api_key"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	if _, err := zsxq.NormalizeAPIKey(input.APIKey); err != nil {
		writeAPIErrorFields(w, http.StatusUnprocessableEntity, "invalid_api_key", "Knowledge Planet API key is invalid", map[string]string{"api_key": "密钥不能为空、不能包含控制字符且不能超过 8 KiB"})
		return
	}
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	account, err := s.zsxqAccounts.UpdateCredential(ctx, input.APIKey)
	if err != nil {
		writeZSXQAPIError(w, err)
		return
	}
	s.events.Publish(service.TopicAccounts)
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) deleteZSXQCredentialV4(w http.ResponseWriter, r *http.Request) {
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

type zsxqGroupView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OwnerID   string `json:"owner_id,omitempty"`
	OwnerName string `json:"owner_name,omitempty"`
}

func (s *Server) zsxqGroupsV4(w http.ResponseWriter, r *http.Request) {
	if s.zsxqAccounts == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "integration_unavailable", "Knowledge Planet integration is unavailable")
		return
	}
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	groups, err := s.zsxqAccounts.Groups(ctx)
	if err != nil {
		if errors.Is(err, zsxq.ErrAuthentication) || errors.Is(err, state.ErrNotFound) {
			s.events.Publish(service.TopicAccounts)
			writeAPIError(w, http.StatusConflict, "account_not_connected", "connect a Knowledge Planet account before listing groups")
			return
		}
		writeZSXQAPIError(w, err)
		return
	}
	views := make([]zsxqGroupView, 0, len(groups))
	for _, group := range groups {
		views = append(views, zsxqGroupView{ID: group.ID, Name: group.Name, OwnerID: group.OwnerID, OwnerName: group.OwnerName})
	}
	writeJSON(w, http.StatusOK, views)
}

func writeZSXQAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, zsxq.ErrRateLimited):
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, zsxq.ErrAuthentication):
		writeAPIErrorFields(w, http.StatusUnprocessableEntity, "invalid_api_key", "Knowledge Planet API key is invalid or expired", map[string]string{"api_key": "密钥无效或已过期"})
	case errors.Is(err, zsxq.ErrPermission):
		writeAPIError(w, http.StatusForbidden, "permission_denied", "Knowledge Planet source permission denied")
	case errors.Is(err, zsxq.ErrSchemaDrift):
		writeAPIError(w, http.StatusBadGateway, "upstream_failure", "Knowledge Planet response schema changed")
	case errors.Is(err, zsxq.ErrUpstream):
		writeAPIError(w, http.StatusBadGateway, "upstream_failure", "Knowledge Planet upstream request failed")
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func (s *Server) sourcesV4(w http.ResponseWriter, r *http.Request) {
	platform := model.Platform(strings.TrimSpace(r.URL.Query().Get("platform")))
	if platform != "" {
		if err := platform.Validate(); err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
	}
	sources, err := s.sourceAdmin.List(r.Context(), platform)
	s.writeAPIResult(w, http.StatusOK, sources, err)
}

func (s *Server) createBilibiliSourceV4(w http.ResponseWriter, r *http.Request) {
	var input struct {
		UID     string `json:"uid"`
		Name    string `json:"name"`
		Note    string `json:"note"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	source := model.Source{ID: model.SourceID(model.PlatformBilibili, input.UID), Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP,
		ExternalID: input.UID, Name: input.Name, Note: input.Note, Enabled: input.Enabled, BaselineState: model.BaselinePending}
	s.createSourceV4(w, r, source)
}

func (s *Server) createZSXQSourceV4(w http.ResponseWriter, r *http.Request) {
	if s.zsxqAccounts == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "integration_unavailable", "Knowledge Planet integration is unavailable")
		return
	}
	var input struct {
		GroupID   string              `json:"group_id"`
		Note      string              `json:"note"`
		Enabled   bool                `json:"enabled"`
		TopicMode model.ZSXQTopicMode `json:"zsxq_topic_mode"`
		Authors   []model.ZSXQAuthor  `json:"zsxq_authors"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	input.Authors = normalizeZSXQAuthors(input.Authors)
	ctx, cancel := withAPITimeout(r)
	defer cancel()
	groups, err := s.zsxqAccounts.Groups(ctx)
	if err != nil {
		if errors.Is(err, zsxq.ErrAuthentication) || errors.Is(err, state.ErrNotFound) {
			s.events.Publish(service.TopicAccounts)
			writeAPIError(w, http.StatusConflict, "account_not_connected", "connect a Knowledge Planet account before adding a source")
			return
		}
		writeZSXQAPIError(w, err)
		return
	}
	groupID := strings.TrimSpace(input.GroupID)
	var selected *zsxq.Group
	for index := range groups {
		if groups[index].ID == groupID {
			selected = &groups[index]
			break
		}
	}
	if selected == nil {
		writeAPIErrorFields(w, http.StatusUnprocessableEntity, "validation_failed", "selected Knowledge Planet group is not visible to the connected account", map[string]string{"group_id": "请选择当前账号中的星球"})
		return
	}
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, selected.ID), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet,
		ExternalID: selected.ID, Name: selected.Name, Note: input.Note, OwnerID: selected.OwnerID, OwnerName: selected.OwnerName,
		Enabled: input.Enabled, BaselineState: model.BaselinePending, ZSXQTopicMode: input.TopicMode, ZSXQAuthors: input.Authors}
	s.createSourceV4(w, r, source)
}

func (s *Server) createSourceV4(w http.ResponseWriter, r *http.Request, source model.Source) {
	if err := source.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	if err := s.sourceAdmin.Create(r.Context(), source); err != nil {
		if errors.Is(err, state.ErrSourceExists) {
			err = conflictFailure(err)
		}
		s.writeAPIResult(w, http.StatusCreated, nil, err)
		return
	}
	setAuditResourceID(r, source.ID)
	details := map[string]any{"platform": source.Platform, "external_id": source.ExternalID, "enabled": source.Enabled}
	if source.Platform == model.PlatformZSXQ {
		details["zsxq_topic_mode"] = source.ZSXQTopicMode
		details["zsxq_author_count"] = len(source.ZSXQAuthors)
	}
	setAuditDetails(r, details)
	s.writeAPIResult(w, http.StatusCreated, source, nil)
}

func (s *Server) updateSourceV4(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string              `json:"name"`
		Note      string              `json:"note"`
		Enabled   bool                `json:"enabled"`
		TopicMode model.ZSXQTopicMode `json:"zsxq_topic_mode"`
		Authors   []model.ZSXQAuthor  `json:"zsxq_authors"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	input.Authors = normalizeZSXQAuthors(input.Authors)
	source, err := s.sourceAdmin.Update(r.Context(), r.PathValue("id"), input.Name, input.Note, input.Enabled, input.TopicMode, input.Authors)
	if err != nil {
		s.writeAPIResult(w, http.StatusOK, nil, err)
		return
	}
	details := map[string]any{"name": source.Name, "note": source.Note, "enabled": source.Enabled}
	if source.Platform == model.PlatformZSXQ {
		details["zsxq_topic_mode"] = source.ZSXQTopicMode
		details["zsxq_author_count"] = len(source.ZSXQAuthors)
	}
	setAuditDetails(r, details)
	s.writeAPIResult(w, http.StatusOK, source, nil)
}

func normalizeZSXQAuthors(authors []model.ZSXQAuthor) []model.ZSXQAuthor {
	for index := range authors {
		authors[index].UserID = strings.TrimSpace(authors[index].UserID)
		authors[index].Name = strings.TrimSpace(authors[index].Name)
	}
	return authors
}

func (s *Server) deleteSourceV4(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.sourceAdmin.Delete(r.Context(), id)
	if err != nil {
		s.writeAPIResult(w, http.StatusNoContent, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) contentsV4(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) contentV4(w http.ResponseWriter, r *http.Request) {
	content, attachments, err := s.store.WithContext(r.Context()).Content(r.PathValue("id"))
	views := make([]attachmentView, 0, len(attachments))
	for _, attachment := range attachments {
		views = append(views, newAttachmentView(attachment))
	}
	s.writeAPIResult(w, http.StatusOK, contentDetailView{Content: content, Attachments: views}, err)
}

func (s *Server) commentTreeV4(w http.ResponseWriter, r *http.Request) {
	tree, incomplete, err := s.store.WithContext(r.Context()).CommentTree(r.PathValue("id"))
	children := make([]commentNodeView, 0, len(tree))
	for _, node := range tree {
		children = append(children, newCommentNodeView(node))
	}
	s.writeAPIResult(w, http.StatusOK, commentTreeView{Children: children, Incomplete: incomplete}, err)
}

type commentTreeView struct {
	Children   []commentNodeView `json:"children"`
	Incomplete bool              `json:"incomplete"`
}

type commentNodeView struct {
	ID          string            `json:"id"`
	Platform    model.Platform    `json:"platform"`
	ContentID   string            `json:"content_id"`
	RootID      string            `json:"root_id,omitempty"`
	ParentID    string            `json:"parent_id,omitempty"`
	AuthorID    string            `json:"author_id,omitempty"`
	Role        model.AuthorRole  `json:"author_role"`
	Name        string            `json:"name"`
	Message     string            `json:"message"`
	PublishedAt time.Time         `json:"published_at"`
	UpdatedAt   time.Time         `json:"updated_at,omitzero"`
	DeletedAt   time.Time         `json:"deleted_at,omitzero"`
	IsTrigger   bool              `json:"is_trigger,omitempty"`
	Media       []attachmentView  `json:"media,omitempty"`
	Children    []commentNodeView `json:"children,omitempty"`
}

func newCommentNodeView(node model.CommentNode) commentNodeView {
	view := commentNodeView{ID: node.ID, Platform: node.Platform, ContentID: node.ContentID, RootID: node.RootID,
		ParentID: node.ParentID, AuthorID: node.AuthorID, Role: node.Role, Name: node.Name, Message: node.Message,
		PublishedAt: node.Time, UpdatedAt: node.UpdatedAt, DeletedAt: node.DeletedAt, IsTrigger: node.IsTrigger}
	for _, attachment := range node.Media {
		view.Media = append(view.Media, newAttachmentView(attachment))
	}
	for _, child := range node.Children {
		view.Children = append(view.Children, newCommentNodeView(child))
	}
	return view
}

type contentDetailView struct {
	Content     model.Content    `json:"content"`
	Attachments []attachmentView `json:"attachments"`
}

type attachmentView struct {
	ID            string               `json:"id"`
	ContentID     string               `json:"content_id"`
	ExternalID    string               `json:"external_id"`
	Type          model.AttachmentType `json:"type"`
	FileName      string               `json:"file_name,omitempty"`
	MIME          string               `json:"mime,omitempty"`
	Size          int64                `json:"size,omitempty"`
	Width         int                  `json:"width,omitempty"`
	Height        int                  `json:"height,omitempty"`
	DurationSec   int64                `json:"duration_sec,omitempty"`
	RemoteHost    string               `json:"remote_host,omitempty"`
	Localized     bool                 `json:"localized"`
	LocalizeError string               `json:"localize_error,omitempty"`
}

func newAttachmentView(attachment model.Attachment) attachmentView {
	return attachmentView{ID: attachment.ID, ContentID: attachment.ContentID, ExternalID: attachment.ExternalID,
		Type: attachment.Type, FileName: attachment.FileName, MIME: attachment.MIME, Size: attachment.Size,
		Width: attachment.Width, Height: attachment.Height, DurationSec: attachment.DurationSec,
		RemoteHost: attachment.RemoteHost, Localized: attachment.LocalPath != "", LocalizeError: attachment.LocalizeError}
}

func (s *Server) attachmentV4(w http.ResponseWriter, r *http.Request) {
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
