package state

import (
	"encoding/json"
	"errors"

	"github.com/linxin2429/bili_notify/model"
)

// Only this encrypted representation contains renewal secrets. Public account
// projections and model JSON must never expose them.
type biliSessionPayload struct {
	Version             int               `json:"version"`
	Cookies             map[string]string `json:"cookies"`
	RefreshToken        string            `json:"refresh_token,omitempty"`
	PendingRefreshToken string            `json:"pending_refresh_token,omitempty"`
}

func encodeBiliSession(session model.BiliSession) biliSessionPayload {
	return biliSessionPayload{Version: 1, Cookies: session.Cookies, RefreshToken: session.RefreshToken, PendingRefreshToken: session.PendingRefreshToken}
}

func (s *Store) openBiliSession(sealed []byte) (model.BiliSession, error) {
	var raw json.RawMessage
	if err := openJSON(s.vault, tablePlatformAccounts, string(model.PlatformBilibili), sealed, &raw); err != nil {
		return model.BiliSession{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return model.BiliSession{}, errors.New("invalid Bilibili session payload")
	}
	var session model.BiliSession
	// Legacy records contain cookie names and string values only.
	if version, ok := fields["version"]; ok && len(version) > 0 && version[0] != '"' {
		var payload biliSessionPayload
		if err := json.Unmarshal(raw, &payload); err != nil || payload.Version != 1 {
			return session, errors.New("unsupported Bilibili session payload")
		}
		session.Cookies, session.RefreshToken, session.PendingRefreshToken = payload.Cookies, payload.RefreshToken, payload.PendingRefreshToken
	} else if err := json.Unmarshal(raw, &session.Cookies); err != nil {
		return session, errors.New("invalid legacy Bilibili session")
	}
	return session, nil
}
