package web

import (
	"encoding/json"
	"net/http"
	"strconv"
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
	mux.HandleFunc("POST /api/v1/bilibili-login", s.requireSession(true, s.startBiliLoginAPI))
	mux.HandleFunc("DELETE /api/v1/bilibili-login/{id}", s.requireSession(true, s.cancelBiliLoginAPI))
	mux.HandleFunc("POST /api/v1/channels/{id}/microsoft-login", s.requireSession(true, s.startMicrosoftLoginAPI))
	mux.HandleFunc("DELETE /api/v1/channels/{id}/microsoft-login", s.requireSession(true, s.cancelMicrosoftLoginAPI))
	mux.HandleFunc("PUT /api/v1/settings", s.requireSession(true, s.updateSettingsAPI))
	mux.HandleFunc("GET /api/v1/dynamics", s.requireSession(false, s.queryDynamicsAPI))
	mux.HandleFunc("GET /api/v1/dynamics/{id}", s.requireSession(false, s.getDynamicAPI))
	mux.HandleFunc("GET /api/v1/comments", s.requireSession(false, s.queryCommentsAPI))
	mux.HandleFunc("GET /api/v1/comments/{rpid}", s.requireSession(false, s.getCommentAPI))
}

func (s *Server) dashboardAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "snapshot.get", nil, http.StatusOK)
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
	s.executeHTTPAction(w, r, "up.create", input, http.StatusCreated)
}

func (s *Server) updateUPAPI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	s.executeHTTPAction(w, r, "up.update", struct {
		UID     string `json:"uid"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}{r.PathValue("uid"), input.Name, input.Enabled}, http.StatusOK)
}

func (s *Server) deleteUPAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "up.delete", struct {
		UID string `json:"uid"`
	}{r.PathValue("uid")}, http.StatusNoContent)
}

func (s *Server) createChannelAPI(w http.ResponseWriter, r *http.Request) {
	var input channelInput
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	s.executeHTTPAction(w, r, "channel.create", input, http.StatusCreated)
}

func (s *Server) updateChannelAPI(w http.ResponseWriter, r *http.Request) {
	var input channelInput
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	input.ID = r.PathValue("id")
	s.executeHTTPAction(w, r, "channel.update", input, http.StatusOK)
}

func (s *Server) deleteChannelAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "channel.delete", idPayload(r.PathValue("id")), http.StatusNoContent)
}

func (s *Server) testChannelAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "channel.test", idPayload(r.PathValue("id")), http.StatusOK)
}

func (s *Server) startBiliLoginAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "bilibili.login.start", nil, http.StatusCreated)
}

func (s *Server) cancelBiliLoginAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "bilibili.login.cancel", idPayload(r.PathValue("id")), http.StatusNoContent)
}

func (s *Server) startMicrosoftLoginAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "microsoft.login.start", channelIDPayload(r.PathValue("id")), http.StatusCreated)
}

func (s *Server) cancelMicrosoftLoginAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "microsoft.login.cancel", channelIDPayload(r.PathValue("id")), http.StatusNoContent)
}

func (s *Server) updateSettingsAPI(w http.ResponseWriter, r *http.Request) {
	var input map[string]any
	if !decodeAPIRequest(w, r, &input) {
		return
	}
	s.executeHTTPAction(w, r, "settings.update", input, http.StatusOK)
}

func (s *Server) queryDynamicsAPI(w http.ResponseWriter, r *http.Request) {
	s.queryContentAPI(w, r, "dynamics.query")
}

func (s *Server) queryCommentsAPI(w http.ResponseWriter, r *http.Request) {
	s.queryContentAPI(w, r, "comments.query")
}

func (s *Server) queryContentAPI(w http.ResponseWriter, r *http.Request, action string) {
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
	s.executeHTTPAction(w, r, action, input, http.StatusOK)
}

func (s *Server) getDynamicAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "dynamics.get", idPayload(r.PathValue("id")), http.StatusOK)
}

func (s *Server) getCommentAPI(w http.ResponseWriter, r *http.Request) {
	s.executeHTTPAction(w, r, "comments.get", struct {
		RPID string `json:"rpid"`
	}{r.PathValue("rpid")}, http.StatusOK)
}

func (s *Server) executeHTTPAction(w http.ResponseWriter, r *http.Request, action string, payload any, successStatus int) {
	raw, err := json.Marshal(payload)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal", "encoding request")
		return
	}
	data, apiErr := s.dispatch(r.Context(), action, raw)
	if apiErr != nil {
		writeAPIError(w, httpStatusForAPIError(apiErr.Code), apiErr.Code, apiErr.Message)
		return
	}
	if successStatus == http.StatusNoContent {
		w.WriteHeader(successStatus)
		return
	}
	writeJSON(w, successStatus, data)
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

func idPayload(id string) struct {
	ID string `json:"id"`
} {
	return struct {
		ID string `json:"id"`
	}{id}
}

func channelIDPayload(id string) struct {
	ChannelID string `json:"channel_id"`
} {
	return struct {
		ChannelID string `json:"channel_id"`
	}{id}
}
