package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service"
	"github.com/linxin2429/bili_notify/state"
	qrcode "github.com/yeqown/go-qrcode/v2"
	"github.com/yeqown/go-qrcode/writer/standard"
	"golang.org/x/sync/errgroup"
)

type wsRequest struct {
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type wsResponse struct {
	ID    string      `json:"id"`
	OK    bool        `json:"ok"`
	Data  any         `json:"data,omitempty"`
	Error *wsAPIError `json:"error,omitempty"`
}

type wsEvent struct {
	Event    string `json:"event"`
	Revision uint64 `json:"revision"`
	Data     any    `json:"data"`
}

type wsAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type dashboardSnapshot struct {
	Status          service.Status                  `json:"status"`
	UPs             []model.UP                      `json:"ups"`
	Channels        []channelView                   `json:"channels"`
	Deliveries      []model.Delivery                `json:"deliveries"`
	BiliLogin       *biliLoginView                  `json:"bili_login,omitempty"`
	MicrosoftLogins []service.MicrosoftLoginSession `json:"microsoft_logins"`
	UpdatedAt       time.Time                       `json:"updated_at"`
}

type biliLoginView struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	QRDataURL string    `json:"qr_data_url,omitempty"`
}

type channelView struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Type              model.ChannelType `json:"type"`
	Enabled           bool              `json:"enabled"`
	Settings          map[string]string `json:"settings"`
	ConfiguredSecrets []string          `json:"configured_secrets"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

type channelInput struct {
	ID       string            `json:"id,omitempty"`
	Name     string            `json:"name"`
	Type     model.ChannelType `json:"type"`
	Enabled  bool              `json:"enabled"`
	Settings map[string]string `json:"settings"`
	Secrets  map[string]string `json:"secrets,omitempty"`
}

type wsWriter struct {
	mu         sync.Mutex
	connection *websocket.Conn
}

func (w *wsWriter) write(ctx context.Context, value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(writeCtx, w.connection, value)
}

func (w *wsWriter) ping(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return w.connection.Ping(pingCtx)
}

func (s *Server) webSocket(w http.ResponseWriter, r *http.Request) {
	token, _, ok := s.auth.validate(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "authentication_required", "authentication required")
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	connection.SetReadLimit(1 << 20)
	s.registerConnection(token, connection)
	defer s.unregisterConnection(token, connection)
	defer connection.Close(websocket.StatusNormalClosure, "connection closed")

	subscription := s.events.Subscribe()
	defer subscription.Close()
	writer := &wsWriter{connection: connection}
	snapshot, err := s.snapshot()
	if err != nil {
		_ = connection.Close(websocket.StatusInternalError, "unable to load dashboard")
		return
	}
	if err := writer.write(r.Context(), wsEvent{Event: "snapshot", Revision: s.events.Revision(), Data: snapshot}); err != nil {
		return
	}

	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() error { return s.readCommands(ctx, token, connection, writer) })
	g.Go(func() error { return s.pushEvents(ctx, subscription, writer) })
	g.Go(func() error {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if _, ok := s.auth.validateToken(token, false); !ok {
					return errors.New("session expired")
				}
				if err := writer.ping(ctx); err != nil {
					return err
				}
			}
		}
	})
	_ = g.Wait()
}

func (s *Server) readCommands(ctx context.Context, token string, connection *websocket.Conn, writer *wsWriter) error {
	for {
		var request wsRequest
		if err := wsjson.Read(ctx, connection, &request); err != nil {
			return err
		}
		if _, ok := s.auth.validateToken(token, true); !ok {
			return errors.New("session expired")
		}
		if request.ID == "" || request.Action == "" {
			if err := writer.write(ctx, wsResponse{ID: request.ID, OK: false, Error: &wsAPIError{Code: "invalid_request", Message: "id and action are required"}}); err != nil {
				return err
			}
			continue
		}
		data, apiErr := s.dispatch(ctx, request.Action, request.Payload)
		response := wsResponse{ID: request.ID, OK: apiErr == nil, Data: data, Error: apiErr}
		if err := writer.write(ctx, response); err != nil {
			return err
		}
	}
}

func (s *Server) pushEvents(ctx context.Context, subscription *service.Subscription, writer *wsWriter) error {
	for {
		topics, revision, err := subscription.Next(ctx)
		if err != nil {
			return err
		}
		if err := s.writeTopicEvents(ctx, writer, topics, revision); err != nil {
			return err
		}
	}
}

func (s *Server) writeTopicEvents(ctx context.Context, writer *wsWriter, topics service.Topic, revision uint64) error {
	if topics&service.TopicStatus != 0 {
		status, err := s.engine.Status()
		if err != nil {
			return err
		}
		if err := writer.write(ctx, wsEvent{Event: "status.updated", Revision: revision, Data: status}); err != nil {
			return err
		}
	}
	if topics&service.TopicUPs != 0 {
		ups, err := s.store.ListUPs()
		if err != nil {
			return err
		}
		if err := writer.write(ctx, wsEvent{Event: "ups.updated", Revision: revision, Data: ups}); err != nil {
			return err
		}
	}
	if topics&service.TopicChannels != 0 {
		channels, err := s.channelViews()
		if err != nil {
			return err
		}
		if err := writer.write(ctx, wsEvent{Event: "channels.updated", Revision: revision, Data: channels}); err != nil {
			return err
		}
	}
	if topics&service.TopicDeliveries != 0 {
		deliveries, err := s.store.ListDeliveries(100)
		if err != nil {
			return err
		}
		if err := writer.write(ctx, wsEvent{Event: "deliveries.updated", Revision: revision, Data: deliveries}); err != nil {
			return err
		}
	}
	if topics&service.TopicBiliLogin != 0 {
		login, err := s.biliLoginView()
		if err != nil {
			return err
		}
		if err := writer.write(ctx, wsEvent{Event: "bilibili.login.updated", Revision: revision, Data: login}); err != nil {
			return err
		}
	}
	if topics&service.TopicMicrosoftLogin != 0 {
		if err := writer.write(ctx, wsEvent{Event: "microsoft.login.updated", Revision: revision, Data: s.engine.MicrosoftLogins()}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) dispatch(parent context.Context, action string, raw json.RawMessage) (any, *wsAPIError) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	switch action {
	case "snapshot.get":
		snapshot, err := s.snapshot()
		return result(snapshot, err)
	case "up.create":
		var input struct {
			UID     string `json:"uid"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, invalidRequest(err)
		}
		up := model.UP{UID: input.UID, Name: input.Name, Enabled: input.Enabled}
		if _, err := s.store.UP(input.UID); err == nil {
			return nil, &wsAPIError{Code: "conflict", Message: "UP already exists"}
		} else if !errors.Is(err, state.ErrNotFound) {
			return nil, apiError(err)
		}
		if err := s.store.PutUP(up); err != nil {
			return nil, apiError(err)
		}
		s.events.Publish(service.TopicStatus | service.TopicUPs)
		return up, nil
	case "up.update":
		var input struct {
			UID     string `json:"uid"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, invalidRequest(err)
		}
		current, err := s.store.UP(input.UID)
		if err != nil {
			return nil, apiError(err)
		}
		current.Name, current.Enabled = input.Name, input.Enabled
		if err := s.store.PutUP(current); err != nil {
			return nil, apiError(err)
		}
		s.events.Publish(service.TopicStatus | service.TopicUPs)
		return current, nil
	case "up.delete":
		var input struct {
			UID string `json:"uid"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, invalidRequest(err)
		}
		if err := s.store.DeleteUP(input.UID); err != nil {
			return nil, apiError(err)
		}
		s.events.Publish(service.TopicStatus | service.TopicUPs)
		return map[string]string{"uid": input.UID}, nil
	case "channel.create", "channel.update":
		var input channelInput
		if err := decodePayload(raw, &input); err != nil {
			return nil, invalidRequest(err)
		}
		channel, err := s.saveChannel(input, action == "channel.update")
		if err != nil {
			return nil, apiError(err)
		}
		s.events.Publish(service.TopicStatus | service.TopicChannels | service.TopicDeliveries | service.TopicMicrosoftLogin)
		return toChannelView(channel), nil
	case "channel.delete":
		var input struct {
			ID string `json:"id"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, invalidRequest(err)
		}
		if err := s.store.DeleteChannel(input.ID); err != nil {
			return nil, apiError(err)
		}
		s.engine.CancelMicrosoftLogin(input.ID)
		s.events.Publish(service.TopicStatus | service.TopicChannels | service.TopicMicrosoftLogin)
		return map[string]string{"id": input.ID}, nil
	case "channel.test":
		var input struct {
			ID string `json:"id"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, invalidRequest(err)
		}
		if err := s.engine.TestChannel(ctx, input.ID); err != nil {
			return nil, &wsAPIError{Code: "upstream_failure", Message: err.Error()}
		}
		return map[string]string{"status": "sent"}, nil
	case "bilibili.login.start":
		login, err := s.engine.StartLogin(ctx)
		if err != nil {
			return nil, apiError(err)
		}
		view, err := s.biliLoginViewFor(login)
		return result(view, err)
	case "bilibili.login.cancel":
		var input struct {
			ID string `json:"id"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, invalidRequest(err)
		}
		s.engine.CancelLogin(input.ID)
		return map[string]string{"id": input.ID}, nil
	case "microsoft.login.start", "microsoft.login.cancel":
		var input struct {
			ChannelID string `json:"channel_id"`
		}
		if err := decodePayload(raw, &input); err != nil {
			return nil, invalidRequest(err)
		}
		if action == "microsoft.login.cancel" {
			s.engine.CancelMicrosoftLogin(input.ChannelID)
			return map[string]string{"channel_id": input.ChannelID}, nil
		}
		login, err := s.engine.StartMicrosoftLogin(ctx, input.ChannelID)
		return result(login, err)
	default:
		return nil, &wsAPIError{Code: "unknown_action", Message: "unknown action"}
	}
}

func (s *Server) snapshot() (dashboardSnapshot, error) {
	status, err := s.engine.Status()
	if err != nil {
		return dashboardSnapshot{}, err
	}
	ups, err := s.store.ListUPs()
	if err != nil {
		return dashboardSnapshot{}, err
	}
	channels, err := s.channelViews()
	if err != nil {
		return dashboardSnapshot{}, err
	}
	deliveries, err := s.store.ListDeliveries(100)
	if err != nil {
		return dashboardSnapshot{}, err
	}
	login, err := s.biliLoginView()
	if err != nil {
		return dashboardSnapshot{}, err
	}
	return dashboardSnapshot{
		Status: status, UPs: ups, Channels: channels, Deliveries: deliveries, BiliLogin: login,
		MicrosoftLogins: s.engine.MicrosoftLogins(), UpdatedAt: time.Now().UTC(),
	}, nil
}

func (s *Server) channelViews() ([]channelView, error) {
	channels, err := s.store.ListChannels()
	if err != nil {
		return nil, err
	}
	views := make([]channelView, 0, len(channels))
	for _, channel := range channels {
		views = append(views, toChannelView(channel))
	}
	return views, nil
}

var secretSettings = map[string]bool{
	"password": true, "secret": true, "webhook": true, "access_token": true, "refresh_token": true,
}

func toChannelView(channel model.Channel) channelView {
	settings := make(map[string]string)
	configured := make([]string, 0)
	for key, value := range channel.Settings {
		if secretSettings[key] {
			if value != "" {
				configured = append(configured, key)
			}
			continue
		}
		settings[key] = value
	}
	slices.Sort(configured)
	return channelView{
		ID: channel.ID, Name: channel.Name, Type: channel.Type, Enabled: channel.Enabled,
		Settings: settings, ConfiguredSecrets: configured, CreatedAt: channel.CreatedAt, UpdatedAt: channel.UpdatedAt,
	}
}

func (s *Server) saveChannel(input channelInput, update bool) (model.Channel, error) {
	settings := make(map[string]string)
	var current model.Channel
	if !update && input.ID != "" {
		return model.Channel{}, errors.New("channel id must be empty when creating a channel")
	}
	if update {
		if input.ID == "" {
			return model.Channel{}, errors.New("channel id is required")
		}
		var err error
		current, err = s.store.Channel(input.ID)
		if err != nil {
			return model.Channel{}, err
		}
		if current.Type == input.Type {
			for key, value := range current.Settings {
				if secretSettings[key] || (current.Type == model.ChannelMicrosoft && slices.Contains([]string{"authorized", "token_type", "token_expiry"}, key)) {
					settings[key] = value
				}
			}
		}
	}
	for key, value := range input.Settings {
		if secretSettings[key] {
			return model.Channel{}, fmt.Errorf("secret setting %q must be sent in secrets", key)
		}
		settings[key] = strings.TrimSpace(value)
	}
	for key, value := range input.Secrets {
		if !secretSettings[key] || key == "access_token" || key == "refresh_token" {
			return model.Channel{}, fmt.Errorf("unsupported secret setting %q", key)
		}
		if key == "webhook" {
			value = strings.TrimSpace(value)
		}
		settings[key] = value
	}
	if update && current.Type == model.ChannelMicrosoft && input.Type == model.ChannelMicrosoft && microsoftIdentityChanged(current.Settings, settings) {
		for _, key := range []string{"access_token", "refresh_token", "token_type", "token_expiry", "authorized"} {
			delete(settings, key)
		}
	}
	channel := model.Channel{ID: input.ID, Name: strings.TrimSpace(input.Name), Type: input.Type, Enabled: input.Enabled, Settings: settings}
	if update {
		channel.CreatedAt = current.CreatedAt
	}
	updated, err := s.store.PutChannel(channel)
	if err == nil && update {
		_ = s.store.UnblockChannel(input.ID)
	}
	return updated, err
}

func microsoftIdentityChanged(current, update map[string]string) bool {
	currentTenant := strings.TrimSpace(current["tenant"])
	if currentTenant == "" {
		currentTenant = "common"
	}
	updateTenant := strings.TrimSpace(update["tenant"])
	if updateTenant == "" {
		updateTenant = "common"
	}
	return strings.TrimSpace(current["client_id"]) != strings.TrimSpace(update["client_id"]) || currentTenant != updateTenant
}

func (s *Server) biliLoginView() (*biliLoginView, error) {
	login, ok := s.engine.Login()
	if !ok {
		return nil, nil
	}
	view, err := s.biliLoginViewFor(login)
	return &view, err
}

func (s *Server) biliLoginViewFor(login service.LoginSession) (biliLoginView, error) {
	view := biliLoginView{ID: login.Key, Status: string(login.Status), ExpiresAt: login.ExpiresAt}
	if login.Status != "success" && login.Status != "expired" {
		url, err := s.engine.LoginURL(login.Key)
		if err != nil {
			return biliLoginView{}, err
		}
		dataURL, err := qrDataURL(url)
		if err != nil {
			return biliLoginView{}, err
		}
		view.QRDataURL = dataURL
	}
	return view, nil
}

type bufferCloser struct{ bytes.Buffer }

func (b *bufferCloser) Close() error { return nil }

func qrDataURL(value string) (string, error) {
	code, err := qrcode.New(value)
	if err != nil {
		return "", fmt.Errorf("creating QR code: %w", err)
	}
	buffer := new(bufferCloser)
	writer := standard.NewWithWriter(buffer, standard.WithQRWidth(8))
	if err := code.Save(writer); err != nil {
		return "", fmt.Errorf("rendering QR code: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes()), nil
}

func decodePayload(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return errors.New("invalid action payload")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("action payload must contain one JSON value")
	}
	return nil
}

func result(value any, err error) (any, *wsAPIError) {
	if err != nil {
		return nil, apiError(err)
	}
	return value, nil
}

func invalidRequest(err error) *wsAPIError {
	return &wsAPIError{Code: "invalid_request", Message: err.Error()}
}

func apiError(err error) *wsAPIError {
	if errors.Is(err, state.ErrNotFound) {
		return &wsAPIError{Code: "not_found", Message: "resource not found"}
	}
	message := err.Error()
	if strings.Contains(message, "pending deliveries") || strings.Contains(message, "already exists") {
		return &wsAPIError{Code: "conflict", Message: message}
	}
	return &wsAPIError{Code: "validation_failed", Message: message}
}
