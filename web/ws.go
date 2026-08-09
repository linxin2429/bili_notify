package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
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
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
)

type wsEvent struct {
	Event    string   `json:"event"`
	Revision uint64   `json:"revision"`
	Topics   []string `json:"topics"`
}

type wsAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type classifiedAPIError struct {
	code string
	err  error
}

func (e *classifiedAPIError) Error() string { return e.err.Error() }
func (e *classifiedAPIError) Unwrap() error { return e.err }

func validationFailure(err error) error {
	return &classifiedAPIError{code: "validation_failed", err: err}
}

func conflictFailure(err error) error {
	return &classifiedAPIError{code: "conflict", err: err}
}

type deliveryView struct {
	ID        string                  `json:"id"`
	Kind      model.DeliveryKind      `json:"kind,omitempty"`
	Dynamic   dynamicPreview          `json:"dynamic,omitzero"`
	Comment   *commentDeliveryPreview `json:"comment,omitempty"`
	ChannelID string                  `json:"channel_id"`
	State     model.DeliveryState     `json:"state"`
	Attempts  int                     `json:"attempts"`
	NextAt    time.Time               `json:"next_at"`
	LastError string                  `json:"last_error,omitempty"`
	CreatedAt time.Time               `json:"created_at"`
}

type dynamicPreview struct {
	ID          string    `json:"id"`
	UID         string    `json:"uid"`
	UPName      string    `json:"up_name"`
	Type        string    `json:"type"`
	PublishedAt time.Time `json:"published_at"`
	Summary     string    `json:"summary"`
	URL         string    `json:"url"`
}

type commentDeliveryPreview struct {
	RPID         string    `json:"rpid"`
	UPUID        string    `json:"up_uid"`
	UPName       string    `json:"up_name"`
	ContentType  string    `json:"content_type"`
	ContentID    string    `json:"content_id"`
	ContentTitle string    `json:"content_title,omitempty"`
	ContentURL   string    `json:"content_url"`
	PublishedAt  time.Time `json:"published_at"`
}

type contentQueryInput struct {
	UID    string `json:"uid,omitempty"`
	Q      string `json:"q,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type dynamicHistoryView struct {
	ID           string                `json:"id"`
	UID          string                `json:"uid"`
	UPName       string                `json:"up_name"`
	Type         string                `json:"type"`
	PublishedAt  time.Time             `json:"published_at"`
	DiscoveredAt time.Time             `json:"discovered_at"`
	Baseline     bool                  `json:"baseline"`
	Title        string                `json:"title,omitempty"`
	Summary      string                `json:"summary,omitempty"`
	URL          string                `json:"url,omitempty"`
	TargetURL    string                `json:"target_url,omitempty"`
	Badge        string                `json:"badge,omitempty"`
	Description  string                `json:"description,omitempty"`
	Media        []model.DynamicMedia  `json:"media,omitempty"`
	Stats        *model.DynamicStats   `json:"stats,omitempty"`
	Video        *model.DynamicVideo   `json:"video,omitempty"`
	Original     *state.DynamicPreview `json:"original,omitempty"`
}

type commentHistoryView struct {
	RPID         string    `json:"rpid"`
	UPUID        string    `json:"up_uid"`
	UPName       string    `json:"up_name"`
	ContentType  string    `json:"content_type,omitempty"`
	ContentID    string    `json:"content_id,omitempty"`
	ContentTitle string    `json:"content_title,omitempty"`
	ContentURL   string    `json:"content_url,omitempty"`
	PublishedAt  time.Time `json:"published_at"`
	DiscoveredAt time.Time `json:"discovered_at"`
	Baseline     bool      `json:"baseline"`
	Incomplete   bool      `json:"incomplete,omitempty"`
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
	timeout    time.Duration
}

func (w *wsWriter) operationTimeout() time.Duration {
	if w.timeout > 0 {
		return w.timeout
	}
	return 10 * time.Second
}

func (w *wsWriter) write(ctx context.Context, value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, w.operationTimeout())
	defer cancel()
	return wsjson.Write(writeCtx, w.connection, value)
}

func (w *wsWriter) ping(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	pingCtx, cancel := context.WithTimeout(ctx, w.operationTimeout())
	defer cancel()
	return w.connection.Ping(pingCtx)
}

func (s *Server) websocketHeartbeat() time.Duration {
	if s.wsHeartbeat > 0 {
		return s.wsHeartbeat
	}
	return 30 * time.Second
}

func (s *Server) websocketPingTimeout() time.Duration {
	if s.wsPingTimeout > 0 {
		return s.wsPingTimeout
	}
	return 10 * time.Second
}

func (s *Server) webSocket(w http.ResponseWriter, r *http.Request) {
	endHandshake := func() {}
	if s.tracer != nil {
		ctx := r.Context()
		if s.propagator != nil {
			ctx = s.propagator.Extract(ctx, propagation.HeaderCarrier(r.Header))
		}
		_, span := s.tracer.Start(ctx, "GET /api/v2/ws", trace.WithSpanKind(trace.SpanKindServer))
		endHandshake = func() { span.End() }
	}
	token, _, ok := s.auth.validate(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "authentication_required", "authentication required")
		endHandshake()
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		endHandshake()
		return
	}
	endHandshake()
	connection.SetReadLimit(1 << 20)
	s.registerConnection(token, connection)
	defer s.unregisterConnection(token, connection)
	defer connection.Close(websocket.StatusNormalClosure, "connection closed")

	subscription := s.events.Subscribe()
	defer subscription.Close()
	writer := &wsWriter{connection: connection, timeout: s.websocketPingTimeout()}
	if err := writer.write(r.Context(), wsEvent{Event: "sync.required", Revision: s.events.Revision(), Topics: allResourceTopics()}); err != nil {
		return
	}

	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() error {
		for {
			if _, _, err := connection.Read(ctx); err != nil {
				return err
			}
		}
	})
	g.Go(func() error { return s.pushEvents(ctx, subscription, writer) })
	g.Go(func() error {
		ticker := time.NewTicker(s.websocketHeartbeat())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if _, ok := s.auth.validateToken(token, false); !ok {
					_ = connection.Close(websocket.StatusPolicyViolation, "session expired")
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
	resources := resourceTopics(topics)
	if len(resources) == 0 {
		return nil
	}
	return writer.write(ctx, wsEvent{Event: "resources.invalidated", Revision: revision, Topics: resources})
}

func allResourceTopics() []string {
	return []string{
		"runtime", "settings", "ups", "channels", "deliveries",
		"bilibili-login", "microsoft-logins", "dynamics", "comments", "audit-logs",
	}
}

func resourceTopics(topics service.Topic) []string {
	resources := make([]string, 0, 10)
	for _, topic := range []struct {
		mask service.Topic
		name string
	}{
		{service.TopicStatus, "runtime"},
		{service.TopicSettings, "settings"},
		{service.TopicUPs, "ups"},
		{service.TopicChannels, "channels"},
		{service.TopicDeliveries, "deliveries"},
		{service.TopicBiliLogin, "bilibili-login"},
		{service.TopicMicrosoftLogin, "microsoft-logins"},
		{service.TopicDynamics, "dynamics"},
		{service.TopicComments, "comments"},
		{service.TopicAuditLogs, "audit-logs"},
	} {
		if topics&topic.mask != 0 {
			resources = append(resources, topic.name)
		}
	}
	return resources
}

func deliveryViews(deliveries []model.Delivery) []deliveryView {
	views := make([]deliveryView, 0, len(deliveries))
	for _, delivery := range deliveries {
		view := deliveryView{
			ID: delivery.ID, Kind: delivery.EffectiveKind(),
			ChannelID: delivery.ChannelID, State: delivery.State, Attempts: delivery.Attempts,
			NextAt: delivery.NextAt, LastError: delivery.LastError, CreatedAt: delivery.CreatedAt,
		}
		if delivery.EffectiveKind() == model.DeliveryKindComment && delivery.Comment != nil {
			view.Comment = &commentDeliveryPreview{
				RPID: delivery.Comment.RPID, UPUID: delivery.Comment.UPUID, UPName: delivery.Comment.UPName,
				ContentType: delivery.Comment.ContentType, ContentID: delivery.Comment.ContentID,
				ContentTitle: delivery.Comment.ContentTitle, ContentURL: delivery.Comment.ContentURL,
				PublishedAt: delivery.Comment.PublishedAt,
			}
		} else {
			view.Dynamic = dynamicPreview{
				ID: delivery.Dynamic.ID, UID: delivery.Dynamic.UID, UPName: delivery.Dynamic.UPName,
				Type: delivery.Dynamic.Type, PublishedAt: delivery.Dynamic.PublishedAt,
				Summary: previewText(delivery.Dynamic.Summary, 240), URL: delivery.Dynamic.URL,
			}
		}
		views = append(views, view)
	}
	return views
}

func parseContentQuery(input contentQueryInput) (state.ContentQuery, error) {
	q := state.ContentQuery{UID: strings.TrimSpace(input.UID), Q: input.Q, Limit: input.Limit, Offset: input.Offset}
	if input.From != "" {
		from, err := time.Parse(time.RFC3339, input.From)
		if err != nil {
			return state.ContentQuery{}, fmt.Errorf("from must be RFC3339: %w", err)
		}
		q.From = from
	}
	if input.To != "" {
		to, err := time.Parse(time.RFC3339, input.To)
		if err != nil {
			return state.ContentQuery{}, fmt.Errorf("to must be RFC3339: %w", err)
		}
		q.To = to
	}
	if !q.From.IsZero() && !q.To.IsZero() && !q.From.Before(q.To) {
		return state.ContentQuery{}, errors.New("from must be earlier than to")
	}
	return q, nil
}

const (
	historyPreviewTextLimit  = 2000
	historyPreviewMediaLimit = 9
)

func toDynamicHistoryView(item state.DynamicRecord) dynamicHistoryView {
	return dynamicHistoryView{
		ID: item.ID, UID: item.UID, UPName: item.UPName, Type: item.Type,
		PublishedAt: item.PublishedAt, DiscoveredAt: item.DiscoveredAt, Baseline: item.Baseline,
		Title: item.Title, Summary: previewText(item.Summary, historyPreviewTextLimit),
		Description: previewText(item.Description, historyPreviewTextLimit),
		URL:         item.URL, TargetURL: item.TargetURL, Badge: item.Badge,
		Media:    boundHistoryMedia(item.ID, item.Media, historyPreviewMediaLimit),
		Stats:    item.Stats,
		Video:    item.Video,
		Original: boundHistoryOriginal(item.Original),
	}
}

func boundHistoryMedia(dynamicID string, mediaItems []model.DynamicMedia, limit int) []model.DynamicMedia {
	if limit <= 0 || len(mediaItems) == 0 {
		return nil
	}
	if len(mediaItems) > limit {
		mediaItems = mediaItems[:limit]
	}
	out := make([]model.DynamicMedia, 0, len(mediaItems))
	for index, item := range mediaItems {
		item.URL = strings.TrimSpace(item.URL)
		if item.LocalPath != "" && dynamicID != "" {
			item.URL = "/api/v2/dynamics/" + url.PathEscape(dynamicID) + "/media/" + strconv.Itoa(index)
			item.LocalPath = ""
			item.ContentType = ""
			item.Size = 0
		}
		if item.URL == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boundHistoryOriginal(original *state.DynamicPreview) *state.DynamicPreview {
	if original == nil {
		return nil
	}
	return &state.DynamicPreview{
		ID: original.ID, UID: original.UID, UPName: original.UPName, Type: original.Type,
		Title:       original.Title,
		Summary:     previewText(original.Summary, historyPreviewTextLimit),
		Description: previewText(original.Description, historyPreviewTextLimit),
		URL:         original.URL, TargetURL: original.TargetURL, Badge: original.Badge,
		Media: boundHistoryMedia(original.ID, original.Media, historyPreviewMediaLimit),
		Video: original.Video,
	}
}

func toCommentHistoryView(item state.CommentRecord) commentHistoryView {
	return commentHistoryView{
		RPID: item.RPID, UPUID: item.UPUID, UPName: item.UPName,
		ContentType: item.ContentType, ContentID: item.ContentID,
		ContentTitle: item.ContentTitle, ContentURL: item.ContentURL,
		PublishedAt: item.PublishedAt, DiscoveredAt: item.DiscoveredAt,
		Baseline: item.Baseline, Incomplete: item.Incomplete,
	}
}

func previewText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
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
	"password": true, "secret": true, "webhook": true, "access_token": true, "refresh_token": true, "app_secret": true,
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
		return model.Channel{}, validationFailure(errors.New("channel id must be empty when creating a channel"))
	}
	if update {
		if input.ID == "" {
			return model.Channel{}, validationFailure(errors.New("channel id is required"))
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
			return model.Channel{}, validationFailure(fmt.Errorf("secret setting %q must be sent in secrets", key))
		}
		settings[key] = strings.TrimSpace(value)
	}
	for key, value := range input.Secrets {
		if !secretSettings[key] || key == "access_token" || key == "refresh_token" {
			return model.Channel{}, validationFailure(fmt.Errorf("unsupported secret setting %q", key))
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
	if err := channel.Validate(); err != nil {
		return model.Channel{}, validationFailure(err)
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

func apiError(err error) *wsAPIError {
	var classified *classifiedAPIError
	if errors.As(err, &classified) {
		return &wsAPIError{Code: classified.code, Message: classified.err.Error()}
	}
	if errors.Is(err, state.ErrNotFound) {
		return &wsAPIError{Code: "not_found", Message: "resource not found"}
	}
	message := err.Error()
	if strings.Contains(message, "pending deliveries") || strings.Contains(message, "already exists") {
		return &wsAPIError{Code: "conflict", Message: message}
	}
	return &wsAPIError{Code: "internal", Message: "internal server error"}
}

func localTimezoneName() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return tz
	}
	name := time.Local.String()
	if name != "" && name != "Local" {
		return name
	}
	// Fall back to a fixed-offset label when the process only knows "Local".
	_, offset := time.Now().Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, offset/3600, (offset%3600)/60)
}
