package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/notify"
	"github.com/linxin2429/bili_notify/state"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

const (
	sessionValidationInterval = 10 * time.Minute
)

type Engine struct {
	store              *state.Store
	client             *bilibili.Client
	media              *media.Downloader
	notificationClient *http.Client
	logger             *slog.Logger
	metrics            *Metrics
	tracer             trace.Tracer
	settingsMu         sync.RWMutex
	settings           model.RuntimeSettings
	settingsChanged    chan struct{}
	limiter            *rate.Limiter
	relationNotify     chan struct{}
	httpTimeout        time.Duration
	sessionMu          sync.RWMutex
	accountMu          sync.RWMutex
	account            model.BiliAccount
	authValid          atomic.Bool
	authEverValid      atomic.Bool
	lastSuccess        atomic.Int64
	riskUntil          atomic.Int64
	clockStatusMu      sync.Mutex
	clockStatus        clockStatus
	clockStatusKnown   bool
	backlogAlerted     atomic.Bool
	loginMu            sync.Mutex
	login              *LoginSession
	loginCancel        context.CancelFunc
	loginWG            sync.WaitGroup
	runCtx             context.Context
	microsoftSendMu    sync.Mutex
	microsoftLoginMu   sync.Mutex
	microsoftLoginWG   sync.WaitGroup
	microsoftRunCtx    context.Context
	microsoftLogins    map[string]*MicrosoftLoginSession
	events             *EventBus
}

type EngineOption func(*Engine)

// WithNotificationHTTPClient sets the client used by HTTP-backed notification channels.
func WithNotificationHTTPClient(client *http.Client) EngineOption {
	return func(engine *Engine) {
		engine.notificationClient = client
	}
}

// WithTracerProvider instruments workflow and delivery operations.
func WithTracerProvider(provider trace.TracerProvider) EngineOption {
	return func(engine *Engine) {
		engine.tracer = provider.Tracer("github.com/linxin2429/bili_notify/service")
	}
}

func NewEngine(store *state.Store, client *bilibili.Client, logger *slog.Logger, metrics *Metrics, settings model.RuntimeSettings, events *EventBus, mediaDownloader *media.Downloader, options ...EngineOption) *Engine {
	if events == nil {
		events = NewEventBus()
	}
	engine := &Engine{
		store: store, client: client, media: mediaDownloader, logger: logger, metrics: metrics,
		settings: settings, settingsChanged: make(chan struct{}),
		limiter:        rate.NewLimiter(rate.Limit(settings.BilibiliRequestRate), max(1, int(settings.BilibiliRequestRate))),
		relationNotify: make(chan struct{}, 1), httpTimeout: 10 * time.Second,
		microsoftLogins: make(map[string]*MicrosoftLoginSession), events: events,
		tracer: tracenoop.NewTracerProvider().Tracer("github.com/linxin2429/bili_notify/service"),
	}
	for _, option := range options {
		option(engine)
	}
	metrics.ApplySettings(settings)
	return engine
}

// Metrics returns the engine's process metrics for HTTP boundary instrumentation.
func (e *Engine) Metrics() *Metrics { return e.metrics }

// ClearBilibiliSession disconnects only the Bilibili platform. Notification,
// AI and Knowledge Planet workflows retain their independent state.
func (e *Engine) ClearBilibiliSession() error {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	if err := e.store.ClearSession(); err != nil {
		return err
	}
	e.client.ClearSession()
	e.setAuth(false)
	e.accountMu.Lock()
	e.account = model.BiliAccount{}
	e.accountMu.Unlock()
	e.publish(TopicStatus)
	return nil
}

func (e *Engine) Settings() model.RuntimeSettings {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.settings
}

// ApplySettings updates all future scheduling decisions without canceling work
// already in flight. Callers must validate and persist settings first.
func (e *Engine) ApplySettings(settings model.RuntimeSettings) {
	if e.Settings() == settings {
		return
	}
	e.settingsMu.Lock()
	e.settings = settings
	e.limiter.SetLimit(rate.Limit(settings.BilibiliRequestRate))
	e.limiter.SetBurst(max(1, int(settings.BilibiliRequestRate)))
	close(e.settingsChanged)
	e.settingsChanged = make(chan struct{})
	e.settingsMu.Unlock()
	e.metrics.ApplySettings(settings)
}

func (e *Engine) settingsSnapshot() (model.RuntimeSettings, <-chan struct{}) {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.settings, e.settingsChanged
}

func (e *Engine) currentCommentSettings() (enabled bool, trackN int) {
	settings := e.Settings()
	return settings.BilibiliCommentsEnabled, settings.BilibiliCommentTrackN
}

type LoginSession struct {
	Key       string            `json:"id"`
	URL       string            `json:"-"`
	Status    bilibili.QRStatus `json:"status"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type MicrosoftLoginSession struct {
	ChannelID               string    `json:"channel_id"`
	Status                  string    `json:"status"`
	UserCode                string    `json:"user_code,omitempty"`
	VerificationURI         string    `json:"verification_uri,omitempty"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	ExpiresAt               time.Time `json:"expires_at,omitzero"`
	Error                   string    `json:"error,omitempty"`

	auth   *notify.MicrosoftDeviceAuth
	cancel context.CancelFunc
}

var ErrMicrosoftLoginNotFound = errors.New("Microsoft login session not found")

type Status struct {
	AuthValid       bool               `json:"auth_valid"`
	BiliAccount     *model.BiliAccount `json:"bili_account,omitempty"`
	LastSuccessAt   time.Time          `json:"last_success_at,omitzero"`
	UPCount         int                `json:"up_count"`
	ChannelCount    int                `json:"channel_count"`
	OutboxDepth     int                `json:"outbox_depth"`
	OldestDelivery  time.Time          `json:"oldest_delivery,omitzero"`
	Ready           bool               `json:"ready"`
	RiskPausedUntil time.Time          `json:"risk_paused_until,omitzero"`
}

type clockStatus struct {
	Ready           bool
	RiskPausedUntil int64
}

func (e *Engine) Run(ctx context.Context) error {
	store := e.store.WithContext(ctx)
	e.loginMu.Lock()
	e.runCtx = ctx
	e.loginMu.Unlock()
	e.microsoftLoginMu.Lock()
	e.microsoftRunCtx = ctx
	e.microsoftLoginMu.Unlock()
	defer func() {
		e.loginMu.Lock()
		e.runCtx = nil
		if e.loginCancel != nil {
			e.loginCancel()
		}
		e.loginMu.Unlock()
		e.loginWG.Wait()
		e.microsoftLoginMu.Lock()
		e.microsoftRunCtx = nil
		for _, session := range e.microsoftLogins {
			if session.cancel != nil {
				session.cancel()
			}
		}
		e.microsoftLoginMu.Unlock()
		e.microsoftLoginWG.Wait()
	}()
	if session, err := store.Session(); err == nil {
		e.client.SetSession(session)
		validateCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
		account, validateErr := e.client.ValidateSession(validateCtx)
		cancel()
		if validateErr == nil {
			session.AccountUID = account.UID
			session.AccountName = account.Name
			if err := store.SaveSession(session); err != nil {
				return fmt.Errorf("updating restored Bilibili session identity: %w", err)
			}
			e.setAccount(account)
			e.authEverValid.Store(true)
			e.authValid.Store(true)
			e.metrics.SetAuth(true)
			e.logger.Info("stored Bilibili session restored", "event", "bilibili.session.restored", "result", "success")
		} else {
			e.authEverValid.Store(true)
			if statusErr := store.SetPlatformAccountStatus(model.PlatformBilibili, model.AccountInvalid, "session validation failed"); statusErr != nil {
				return fmt.Errorf("marking invalid Bilibili session: %w", statusErr)
			}
			e.logger.Warn("stored Bilibili session is invalid", "event", "bilibili.session.invalid", "result", "failure", "error", validateErr)
			e.enqueueSystem("B站登录失效，请在管理控制台重新扫码登录。")
		}
	} else if errors.Is(err, context.Canceled) {
		return nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("loading Bilibili session: %w", err)
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.collectLoop(ctx) })
	g.Go(func() error { return e.commentLoop(ctx) })
	g.Go(func() error { return e.deliveryLoop(ctx) })
	g.Go(func() error { return e.authLoop(ctx) })
	g.Go(func() error { return e.relationLoop(ctx) })
	return g.Wait()
}

// Running reports whether Run has installed its lifecycle context. It is used
// by embedding applications to avoid accepting interactive login work before
// the engine is ready.
func (e *Engine) Running() bool {
	e.loginMu.Lock()
	defer e.loginMu.Unlock()
	return e.runCtx != nil
}

func (e *Engine) collectLoop(ctx context.Context) error {
	settings, settingsChanged := e.settingsSnapshot()
	interval := settings.PollInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if err := e.collectOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		e.logger.Error("initial collection cycle failed", "event", "collection.cycle.failed", "phase", "initial", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-settingsChanged:
			settings, settingsChanged = e.settingsSnapshot()
			next := settings.PollInterval()
			if next != interval {
				ticker.Reset(next)
				interval = next
			}
		case <-ticker.C:
			if err := e.collectOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				e.logger.Error("collection cycle failed", "event", "collection.cycle.failed", "error", err)
			}
		}
	}
}

func (e *Engine) relationLoop(ctx context.Context) error {
	settings, settingsChanged := e.settingsSnapshot()
	interval := settings.RelationRefreshInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	run := func() {
		if err := e.refreshRelations(ctx); err != nil && !errors.Is(err, context.Canceled) {
			e.logger.Warn("Bilibili follow relations refresh failed", "event", "bilibili.relations.refresh_failed", "error", err)
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-settingsChanged:
			settings, settingsChanged = e.settingsSnapshot()
			next := settings.RelationRefreshInterval()
			if next != interval {
				ticker.Reset(next)
				interval = next
			}
		case <-ticker.C:
			run()
		case <-e.relationNotify:
			run()
		}
	}
}

func (e *Engine) refreshRelations(ctx context.Context) (err error) {
	ctx, span := e.tracer.Start(ctx, "relations.refresh")
	defer func() { finishSpan(span, err) }()
	store := e.store.WithContext(ctx)
	if !e.authValid.Load() {
		return nil
	}
	if until := e.riskUntil.Load(); until > time.Now().Unix() {
		return nil
	}
	account := e.currentAccount()
	if account.UID == "" {
		return nil
	}
	ups, err := store.ListUPs()
	if err != nil {
		return fmt.Errorf("listing UPs for follow relations: %w", err)
	}
	if len(ups) == 0 {
		return nil
	}
	states := make(map[string]model.FollowState, len(ups))
	uids := make([]string, 0, len(ups))
	for _, up := range ups {
		states[up.UID] = model.FollowUnknown
		uids = append(uids, up.UID)
	}
	e.sessionMu.RLock()
	defer e.sessionMu.RUnlock()
	for start := 0; start < len(uids); start += 50 {
		end := min(start+50, len(uids))
		if err := e.limiter.Wait(ctx); err != nil {
			return err
		}
		requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
		batch, fetchErr := e.client.FetchRelations(requestCtx, uids[start:end])
		cancel()
		if fetchErr != nil {
			e.handleBiliAPIError(fetchErr)
			if err := store.PutFollowRelations(account.UID, states, time.Now()); err != nil {
				return fmt.Errorf("recording unknown follow relations: %w", err)
			}
			e.publish(TopicUPs)
			return fetchErr
		}
		for uid, state := range batch {
			states[uid] = state
		}
	}
	if err := store.PutFollowRelations(account.UID, states, time.Now()); err != nil {
		return fmt.Errorf("recording follow relations: %w", err)
	}
	e.publish(TopicUPs)
	e.logger.DebugContext(ctx, "Bilibili follow relations refreshed", "event", "bilibili.relations.refreshed", "account_uid", account.UID, "up_count", len(uids))
	return nil
}

func (e *Engine) handleBiliAPIError(err error) {
	if bilibili.IsAuthentication(err) {
		e.setAuth(false)
	}
	if !bilibili.IsRiskControl(err) {
		return
	}
	now := time.Now()
	pause := e.Settings().RiskPause()
	previousUntil := e.riskUntil.Swap(now.Add(pause).Unix())
	if previousUntil <= now.Unix() {
		e.logger.Warn("Bilibili risk-control pause started", "event", "bilibili.risk_control.started", "resume_at", now.Add(pause), "error", err)
		e.publish(TopicStatus)
		e.enqueueSystem(fmt.Sprintf("B站接口触发风控，采集已暂停 %s；服务不会尝试绕过风控。", pause.Round(time.Second)))
	}
}

func (e *Engine) collectOnce(ctx context.Context) (err error) {
	ctx, span := e.tracer.Start(ctx, "collection.run")
	started := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "error"
		}
		e.metrics.RecordWorkflow(ctx, "collection", result, time.Since(started))
		finishSpan(span, err)
	}()
	store := e.store.WithContext(ctx)
	if err := e.publishClockStatusIfChanged(); err != nil {
		return fmt.Errorf("checking time-derived status: %w", err)
	}
	if !e.authValid.Load() {
		e.logger.DebugContext(ctx, "collection cycle skipped", "event", "collection.cycle.skipped", "reason", "Bilibili session is not authenticated")
		return nil
	}
	if until := e.riskUntil.Load(); until > time.Now().Unix() {
		e.logger.DebugContext(ctx, "collection cycle skipped", "event", "collection.cycle.skipped", "reason", "Bilibili risk-control pause", "resume_at", time.Unix(until, 0))
		return nil
	}
	account := e.currentAccount()
	if account.UID == "" {
		return errors.New("authenticated Bilibili session has no account identity")
	}
	settings := e.Settings()
	ups, err := store.ListUPs()
	if err != nil {
		return fmt.Errorf("listing UPs: %w", err)
	}
	channels, err := store.ListChannels()
	if err != nil {
		return fmt.Errorf("listing channels: %w", err)
	}
	channelIDs := enabledChannelIDs(channels)
	if len(channelIDs) == 0 {
		e.logger.DebugContext(ctx, "collection cycle skipped", "event", "collection.cycle.skipped", "reason", "no enabled notification channels")
		return nil
	}
	relations, err := store.FollowRelations(account.UID)
	if err != nil {
		return fmt.Errorf("listing follow relations: %w", err)
	}
	followedEnabled := false
	for _, up := range ups {
		if up.Enabled && relations[up.UID].State == model.Followed {
			followedEnabled = true
			break
		}
	}
	feedInitialized := false
	if followedEnabled {
		feed, feedErr := store.FeedState(account.UID)
		if errors.Is(feedErr, state.ErrNotFound) || (feedErr == nil && !feed.Initialized) {
			e.sessionMu.RLock()
			initializeErr := e.initializeFeed(ctx, account.UID)
			e.sessionMu.RUnlock()
			if initializeErr != nil {
				e.logger.WarnContext(ctx, "Bilibili aggregate feed baseline failed", "event", "bilibili.feed.baseline_failed", "account_uid", account.UID, "error", initializeErr)
			}
			feed, feedErr = store.FeedState(account.UID)
		}
		if feedErr != nil && !errors.Is(feedErr, state.ErrNotFound) {
			return fmt.Errorf("loading aggregate feed state: %w", feedErr)
		}
		feedInitialized = feed.Initialized
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(settings.BilibiliRequestConcurrency)
	enabledUPs := 0
	feedUPs := make([]model.UP, 0, len(ups))
	now := time.Now()
	for _, up := range ups {
		if !up.Enabled {
			continue
		}
		enabledUPs++
		relation := relations[up.UID]
		feedReady := feedInitialized && relation.State == model.Followed && relation.SpaceSynced && up.BaselineReady && up.ExclusiveBaselineReady
		if feedReady {
			feedUPs = append(feedUPs, up)
		}
		spaceDue := !feedReady || relation.LastSpacePollAt.IsZero() || now.Sub(relation.LastSpacePollAt) >= settings.SpaceReconcileInterval()
		if spaceDue {
			g.Go(func() error { return e.pollUP(gctx, up, channelIDs) })
		}
	}
	if len(feedUPs) > 0 {
		g.Go(func() error { return e.pollFeed(gctx, account, feedUPs, channelIDs) })
	}
	err = g.Wait()
	if err != nil {
		return err
	}
	e.logger.DebugContext(ctx, "collection cycle completed", "event", "collection.cycle.completed", "enabled_ups", enabledUPs, "enabled_channels", len(channelIDs), "duration_ms", elapsedMS(started))
	return nil
}

func (e *Engine) initializeFeed(ctx context.Context, accountUID string) error {
	store := e.store.WithContext(ctx)
	if err := e.limiter.Wait(ctx); err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
	defer cancel()
	page, err := e.client.FetchAllPage(requestCtx, "", "")
	if err != nil {
		e.handleBiliAPIError(err)
		return err
	}
	if page.UpdateBaseline == "" {
		return &bilibili.APIError{Kind: bilibili.ErrorSchema, Message: "aggregate feed baseline is missing"}
	}
	if err := store.InitializeFeed(accountUID, page.UpdateBaseline, time.Now()); err != nil {
		return fmt.Errorf("recording aggregate feed baseline: %w", err)
	}
	e.publish(TopicUPs)
	return nil
}

func (e *Engine) pollFeed(ctx context.Context, account model.BiliAccount, ups []model.UP, channelIDs []string) error {
	store := e.store.WithContext(ctx)
	started := time.Now()
	e.sessionMu.RLock()
	defer e.sessionMu.RUnlock()
	feed, err := store.FeedState(account.UID)
	if err != nil {
		return err
	}
	uids := make([]string, 0, len(ups))
	targets := make(map[string]model.UP, len(ups))
	for _, up := range ups {
		uids = append(uids, up.UID)
		targets[up.UID] = up
	}
	requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
	defer cancel()
	if err := e.limiter.Wait(requestCtx); err != nil {
		return err
	}
	update, err := e.client.CheckFeedUpdate(requestCtx, feed.UpdateBaseline)
	if err != nil {
		return e.failFeed(ctx, ups, started, err)
	}
	if update.UpdateNum == 0 {
		return e.completeFeedPoll(ctx, ups, nil, started, 0)
	}

	var (
		rawItems    []json.RawMessage
		offset      string
		newBaseline string
		updateNum   = -1
	)
	maxPages := e.Settings().BilibiliMaxDynamicPages
	for pageNumber := range maxPages {
		if err := e.limiter.Wait(requestCtx); err != nil {
			return err
		}
		page, fetchErr := e.client.FetchAllPage(requestCtx, feed.UpdateBaseline, offset)
		if fetchErr != nil {
			return e.failFeed(ctx, ups, started, fetchErr)
		}
		if pageNumber == 0 {
			newBaseline = page.UpdateBaseline
			updateNum = page.UpdateNum
			if newBaseline == "" {
				return e.failFeed(ctx, ups, started, &bilibili.APIError{Kind: bilibili.ErrorSchema, Message: "aggregate feed update baseline is missing"})
			}
		}
		remaining := updateNum - len(rawItems)
		if remaining > 0 {
			rawItems = append(rawItems, page.Items[:min(remaining, len(page.Items))]...)
		}
		if len(rawItems) >= updateNum {
			break
		}
		if !page.HasMore || page.Offset == "" || page.Offset == offset {
			return e.failFeed(ctx, ups, started, &bilibili.APIError{Kind: bilibili.ErrorSchema, Message: "aggregate feed ended before update count was reached"})
		}
		if pageNumber == maxPages-1 {
			if resetErr := store.ResetFeed(account.UID, uids, time.Now()); resetErr != nil {
				return fmt.Errorf("resetting aggregate feed after pagination overflow: %w", resetErr)
			}
			e.publish(TopicUPs)
			return e.failFeed(ctx, ups, started, fmt.Errorf("more than %d aggregate feed pages; space resynchronization required", maxPages))
		}
		offset = page.Offset
	}

	rawByUID := make(map[string][]json.RawMessage)
	for _, raw := range rawItems {
		uid, parseErr := bilibili.DynamicAuthorUID(raw)
		if parseErr != nil {
			return e.failFeed(ctx, ups, started, parseErr)
		}
		if _, ok := targets[uid]; ok {
			rawByUID[uid] = append(rawByUID[uid], raw)
		}
	}
	failedUIDs := make([]string, 0)
	dynamicsByUID := make(map[string][]model.Dynamic, len(rawByUID))
	allDynamics := make([]model.Dynamic, 0)
	for uid, raws := range rawByUID {
		group := make([]model.Dynamic, 0, len(raws))
		var groupErr error
		for _, raw := range raws {
			dynamic, parseErr := bilibili.ParseDynamicItem(raw)
			if bilibili.IsDynamicBlocked(parseErr) {
				continue
			}
			if parseErr != nil {
				groupErr = parseErr
				break
			}
			if dynamic.Type != "DYNAMIC_TYPE_LIVE_RCMD" {
				group = append(group, dynamic)
			}
		}
		if groupErr != nil {
			failedUIDs = append(failedUIDs, uid)
			if err := e.failPoll(ctx, targets[uid], targets[uid].Name, started, groupErr); err != nil {
				return err
			}
			continue
		}
		dynamicsByUID[uid] = group
		allDynamics = append(allDynamics, group...)
	}
	slices.SortFunc(allDynamics, func(a, b model.Dynamic) int { return a.PublishedAt.Compare(b.PublishedAt) })
	e.enrichMedia(ctx, allDynamics)
	created, err := store.RecordFeedDynamics(account.UID, newBaseline, allDynamics, channelIDs, failedUIDs)
	if err != nil {
		return err
	}
	if created > 0 {
		e.publish(TopicStatus | TopicDeliveries | TopicDynamics)
	} else if len(allDynamics) > 0 {
		e.publish(TopicDynamics)
	}
	for uid, dynamics := range dynamicsByUID {
		if err := e.refreshCommentTargets(ctx, targets[uid], targets[uid].Name, dynamics); err != nil {
			return err
		}
	}
	successful := make([]model.UP, 0, len(ups)-len(failedUIDs))
	for _, up := range ups {
		if !slices.Contains(failedUIDs, up.UID) {
			successful = append(successful, up)
		}
	}
	if err := e.completeFeedPoll(ctx, successful, allDynamics, started, created); err != nil {
		return err
	}
	if len(failedUIDs) > 0 {
		e.publish(TopicUPs)
	}
	return nil
}

func (e *Engine) completeFeedPoll(ctx context.Context, ups []model.UP, dynamics []model.Dynamic, started time.Time, created int) error {
	store := e.store.WithContext(ctx)
	now := time.Now()
	uids := make([]string, 0, len(ups))
	for _, up := range ups {
		uids = append(uids, up.UID)
	}
	if err := store.SetUPResults(uids, now, nil); err != nil {
		return err
	}
	e.metrics.RecordWorkflow(ctx, "feed_poll", "success", time.Since(started))
	e.metrics.RecordContent(ctx, "dynamic", created, time.Time{}, now)
	e.lastSuccess.Store(now.Unix())
	for _, dynamic := range dynamics {
		e.metrics.RecordContent(ctx, "dynamic", 0, dynamic.PublishedAt, now)
	}
	if created > 0 {
		e.logger.InfoContext(ctx, "aggregate feed dynamics queued", "event", "bilibili.feed.dynamics_queued", "dynamic_count", created, "up_count", len(ups))
	}
	e.logger.DebugContext(ctx, "Bilibili aggregate feed poll succeeded", "event", "bilibili.feed.poll_completed", "result", "success", "up_count", len(ups), "fetched_items", len(dynamics), "duration_ms", elapsedMS(started))
	return nil
}

func (e *Engine) failFeed(ctx context.Context, ups []model.UP, started time.Time, pollErr error) error {
	store := e.store.WithContext(ctx)
	e.handleBiliAPIError(pollErr)
	uids := make([]string, 0, len(ups))
	for _, up := range ups {
		uids = append(uids, up.UID)
	}
	if err := store.SetUPResults(uids, time.Now(), pollErr); err != nil {
		return fmt.Errorf("recording failed aggregate feed poll: %w", err)
	}
	e.metrics.RecordWorkflow(ctx, "feed_poll", "error", time.Since(started))
	e.publish(TopicStatus | TopicUPs)
	e.logger.WarnContext(ctx, "Bilibili aggregate feed poll failed", "event", "bilibili.feed.poll_completed", "result", "failure", "up_count", len(ups), "duration_ms", elapsedMS(started), "error", pollErr)
	return nil
}

func (e *Engine) pollUP(ctx context.Context, up model.UP, channelIDs []string) (err error) {
	ctx, span := e.tracer.Start(ctx, "collection.poll_up", trace.WithAttributes(attribute.String("bili.up.uid", up.UID)))
	defer func() { finishSpan(span, err) }()
	store := e.store.WithContext(ctx)
	started := time.Now()
	e.sessionMu.RLock()
	defer e.sessionMu.RUnlock()
	requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
	defer cancel()
	var (
		items  []model.Dynamic
		offset string
		name   string
	)
	maxPages := e.Settings().BilibiliMaxDynamicPages
	for pageNumber := range maxPages {
		if err := e.limiter.Wait(requestCtx); err != nil {
			return err
		}
		page, err := e.client.FetchPage(requestCtx, up.UID, offset)
		if err != nil {
			e.handleBiliAPIError(err)
			return e.failPoll(ctx, up, name, started, err)
		}
		name = page.UPName
		foundSeen := false
		for _, dynamic := range page.Items {
			seen, err := store.Seen(up.UID, dynamic.ID)
			if err != nil {
				return fmt.Errorf("checking seen dynamic: %w", err)
			}
			if seen {
				foundSeen = true
				// Space dynamics are newest-first. Once the persisted frontier is
				// reached, everything after it is older and must not be rediscovered.
				break
			}
			items = append(items, dynamic)
		}
		if !up.BaselineReady || foundSeen || !page.HasMore {
			break
		}
		if page.Offset == "" || page.Offset == offset {
			err := &bilibili.APIError{Kind: bilibili.ErrorSchema, Message: "space dynamics pagination offset did not advance"}
			return e.failPoll(ctx, up, name, started, err)
		}
		if pageNumber == maxPages-1 {
			err := fmt.Errorf("more than %d pages of unseen dynamics; manual review required", maxPages)
			return e.failPoll(ctx, up, name, started, err)
		}
		offset = page.Offset
	}
	slices.SortFunc(items, func(a, b model.Dynamic) int { return a.PublishedAt.Compare(b.PublishedAt) })
	e.enrichMedia(ctx, items)
	baselineMode := state.DynamicBaselineNone
	if !up.BaselineReady {
		baselineMode = state.DynamicBaselineAll
	} else if !up.ExclusiveBaselineReady {
		baselineMode = state.DynamicBaselineExclusive
	}
	created, err := store.RecordDynamics(up.UID, items, channelIDs, baselineMode)
	if err != nil {
		return fmt.Errorf("recording dynamics: %w", err)
	}
	if created > 0 {
		// RecordDynamics commits content, seen markers, and outbox rows atomically.
		// Publish that committed state before later bookkeeping can fail.
		e.publish(TopicStatus | TopicDeliveries | TopicDynamics)
	} else if len(items) > 0 {
		e.publish(TopicDynamics)
	}
	if !up.BaselineReady {
		// BaselineReady is committed by RecordDynamics as well.
		e.publish(TopicUPs)
	} else if !up.ExclusiveBaselineReady {
		e.logger.InfoContext(ctx, "Bilibili exclusive dynamic baseline established", "event", "bilibili.up.exclusive_baseline_established", "up_uid", up.UID, "up_name", name)
	}
	if err := e.refreshCommentTargets(ctx, up, name, items); err != nil {
		return err
	}
	now := time.Now()
	if err := store.SetUPResult(up.UID, name, now, nil); err != nil {
		return err
	}
	if account := e.currentAccount(); account.UID != "" {
		relations, relationErr := store.FollowRelations(account.UID)
		if relationErr != nil {
			return relationErr
		}
		wasSynced := relations[up.UID].SpaceSynced
		if err := store.MarkSpaceSynced(account.UID, up.UID, now); err != nil {
			return err
		}
		if !wasSynced {
			e.publish(TopicUPs)
		}
	}
	if up.ConsecutiveFail >= 3 {
		e.enqueueSystem(fmt.Sprintf("UP %s 的动态采集已恢复。", up.UID))
	}
	if up.ConsecutiveFail > 0 {
		e.logger.InfoContext(ctx, "Bilibili UP poll recovered", "event", "bilibili.up.poll_recovered", "up_uid", up.UID, "up_name", name, "previous_failures", up.ConsecutiveFail)
	}
	e.metrics.RecordWorkflow(ctx, "up_poll", "success", time.Since(started))
	e.metrics.RecordContent(ctx, "dynamic", created, time.Time{}, now)
	e.lastSuccess.Store(now.Unix())
	if up.BaselineReady && (name != "" && name != up.Name || up.ConsecutiveFail > 0) {
		e.publish(TopicUPs)
	}
	if err := e.publishClockStatusIfChanged(); err != nil {
		return fmt.Errorf("checking time-derived status after poll: %w", err)
	}
	for _, dynamic := range items {
		if up.BaselineReady && !(baselineMode == state.DynamicBaselineExclusive && dynamic.Exclusive) {
			e.metrics.RecordContent(ctx, "dynamic", 0, dynamic.PublishedAt, now)
		}
	}
	if created > 0 {
		e.logger.InfoContext(ctx, "new dynamics queued", "event", "bilibili.up.dynamics_queued", "up_uid", up.UID, "up_name", name, "dynamic_count", created, "channel_count", len(channelIDs))
	} else if !up.BaselineReady {
		e.logger.InfoContext(ctx, "Bilibili UP baseline established", "event", "bilibili.up.baseline_established", "up_uid", up.UID, "up_name", name, "baseline_items", len(items), "duration_ms", elapsedMS(started))
	}
	e.logger.DebugContext(ctx, "Bilibili UP poll succeeded", "event", "bilibili.up.poll_completed", "result", "success", "up_uid", up.UID, "up_name", name, "fetched_items", len(items), "queued_dynamics", created, "duration_ms", elapsedMS(started))
	return nil
}

func (e *Engine) refreshCommentTargets(ctx context.Context, up model.UP, name string, items []model.Dynamic) error {
	store := e.store.WithContext(ctx)
	enabled, trackN := e.currentCommentSettings()
	if !enabled || trackN < 1 {
		return nil
	}
	discovered := make([]model.CommentTarget, 0, len(items))
	for _, dynamic := range items {
		if !dynamic.Commentable {
			continue
		}
		title := dynamic.Title
		if title == "" {
			title = dynamic.Summary
		}
		contentURL := dynamic.TargetURL
		if contentURL == "" {
			contentURL = dynamic.URL
		}
		upName := name
		if upName == "" {
			upName = dynamic.UPName
		}
		if upName == "" {
			upName = up.Name
		}
		discovered = append(discovered, model.CommentTarget{
			UID:          up.UID,
			UPName:       upName,
			DynamicID:    dynamic.ID,
			ContentType:  dynamic.Type,
			Title:        title,
			URL:          contentURL,
			CommentType:  dynamic.CommentType,
			CommentOID:   dynamic.CommentOID,
			PublishedAt:  dynamic.PublishedAt,
			CommentCount: dynamic.CommentCount,
		})
	}
	if len(discovered) == 0 {
		// Still re-trim existing targets if N shrank.
		existing, err := store.ListCommentTargets(up.UID)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			return nil
		}
		_, err = store.UpsertCommentTargets(up.UID, nil, trackN)
		return err
	}
	_, err := store.UpsertCommentTargets(up.UID, discovered, trackN)
	return err
}

func (e *Engine) commentLoop(ctx context.Context) error {
	settings, settingsChanged := e.settingsSnapshot()
	interval := settings.CommentBatchInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if err := e.commentOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		e.logger.Error("initial comment cycle failed", "event", "comment.cycle.failed", "phase", "initial", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-settingsChanged:
			settings, settingsChanged = e.settingsSnapshot()
			next := settings.CommentBatchInterval()
			if next != interval {
				ticker.Reset(next)
				interval = next
			}
			// Apply comment-monitoring changes immediately. Waiting for the old
			// batch boundary makes enabling monitoring or narrowing its window
			// appear ineffective for up to an entire previous interval.
			if err := e.commentOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				e.logger.Error("comment cycle failed", "event", "comment.cycle.failed", "phase", "settings_changed", "error", err)
			}
		case <-ticker.C:
			if err := e.commentOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				e.logger.Error("comment cycle failed", "event", "comment.cycle.failed", "error", err)
			}
		}
	}
}

func (e *Engine) commentOnce(ctx context.Context) (err error) {
	ctx, span := e.tracer.Start(ctx, "comments.run")
	started := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "error"
		}
		e.metrics.RecordWorkflow(ctx, "comments", result, time.Since(started))
		finishSpan(span, err)
	}()
	store := e.store.WithContext(ctx)
	settings := e.Settings()
	if !settings.BilibiliCommentsEnabled {
		return nil
	}
	if !e.authValid.Load() {
		return nil
	}
	if until := e.riskUntil.Load(); until > time.Now().Unix() {
		return nil
	}
	e.sessionMu.RLock()
	defer e.sessionMu.RUnlock()
	channels, err := store.ListChannels()
	if err != nil {
		return fmt.Errorf("listing channels for comments: %w", err)
	}
	channelIDs := enabledChannelIDs(channels)
	if len(channelIDs) == 0 {
		return nil
	}
	ups, err := store.ListUPs()
	if err != nil {
		return fmt.Errorf("listing UPs for comments: %w", err)
	}
	enabledUIDs := make(map[string]model.UP)
	for _, up := range ups {
		if up.Enabled {
			enabledUIDs[up.UID] = up
		}
	}
	targets, err := store.ListAllCommentTargets()
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(settings.BilibiliRequestConcurrency)
	scanned := 0
	for _, target := range targets {
		if _, ok := enabledUIDs[target.UID]; !ok {
			continue
		}
		if target.Closed {
			continue
		}
		scanned++
		g.Go(func() error {
			if err := e.limiter.Wait(gctx); err != nil {
				return err
			}
			return e.pollCommentTarget(gctx, target, channelIDs)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if scanned > 0 {
		e.logger.DebugContext(ctx, "comment cycle completed", "event", "comment.cycle.completed", "targets", scanned)
	}
	return nil
}

func (e *Engine) pollCommentTarget(ctx context.Context, target model.CommentTarget, channelIDs []string) (err error) {
	ctx, span := e.tracer.Start(ctx, "comments.poll_target", trace.WithAttributes(
		attribute.Int("bili.comment.type", target.CommentType),
		attribute.String("bili.comment.oid", target.CommentOID),
	))
	defer func() { finishSpan(span, err) }()
	store := e.store.WithContext(ctx)

	upReplies := make([]bilibili.Reply, 0)
	// rootRPID -> root reply
	roots := make(map[string]bilibili.Reply)
	// child rpid -> reply
	children := make(map[string]bilibili.Reply)
	// roots that need full expansion because an UP reply lives under them
	expandRoots := make(map[string]struct{})
	paginationIncomplete := false

	seenRootPages := make(map[string]bool)
	for pn := 1; pn <= 10000; pn++ {
		if pn > 1 {
			if err := e.limiter.Wait(ctx); err != nil {
				return err
			}
		}
		requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
		page, err := e.client.ListRootReplies(requestCtx, target.CommentType, target.CommentOID, pn, 20)
		cancel()
		if err != nil {
			return e.handleCommentPollError(ctx, target, err)
		}
		signature := replyPageSignature(page.Replies)
		if page.HasMore && (signature == "" || seenRootPages[signature]) {
			paginationIncomplete = true
			break
		}
		seenRootPages[signature] = true
		for _, reply := range page.Replies {
			roots[reply.RPID] = reply
			if reply.Mid == target.UID {
				upReplies = append(upReplies, reply)
			}
			// Preview nested replies sometimes accompany roots; capture lightly.
			_ = reply
		}
		if !page.HasMore {
			break
		}
		if pn == 10000 {
			paginationIncomplete = true
		}
	}

	// A complete tree requires expanding every root that reports children. This
	// also discovers nested UP replies without relying on truncated root previews.
	for rootID, root := range roots {
		if root.RCount <= 0 {
			continue
		}
		expandRoots[rootID] = struct{}{}
	}

	for rootID := range expandRoots {
		seenReplyPages := make(map[string]bool)
		for pn := 1; pn <= 10000; pn++ {
			if err := e.limiter.Wait(ctx); err != nil {
				return err
			}
			requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
			page, err := e.client.ListChildReplies(requestCtx, target.CommentType, target.CommentOID, rootID, pn, 20)
			cancel()
			if err != nil {
				return e.handleCommentPollError(ctx, target, err)
			}
			signature := replyPageSignature(page.Replies)
			if page.HasMore && (signature == "" || seenReplyPages[signature]) {
				paginationIncomplete = true
				break
			}
			seenReplyPages[signature] = true
			for _, reply := range page.Replies {
				if reply.Root == "" {
					reply.Root = rootID
				}
				children[reply.RPID] = reply
				if reply.Mid == target.UID {
					upReplies = append(upReplies, reply)
				}
			}
			if !page.HasMore {
				break
			}
			if pn == 10000 {
				paginationIncomplete = true
			}
		}
	}

	// Deduplicate UP replies by rpid.
	unique := make(map[string]bilibili.Reply, len(upReplies))
	for _, reply := range upReplies {
		unique[reply.RPID] = reply
	}

	notes := make([]model.CommentNotification, 0, len(unique))
	for _, reply := range unique {
		seen, err := store.CommentSeen(target.UID, reply.RPID)
		if err != nil {
			return err
		}
		if seen && target.BaselineReady {
			continue
		}
		thread, incomplete := buildCommentThread(target, reply, roots, children)
		notes = append(notes, model.CommentNotification{
			RPID:         reply.RPID,
			UPUID:        target.UID,
			UPName:       target.UPName,
			ContentType:  target.ContentType,
			ContentID:    target.DynamicID,
			ContentTitle: target.Title,
			ContentURL:   target.URL,
			PublishedAt:  reply.CTime,
			Incomplete:   incomplete || paginationIncomplete,
			Thread:       thread,
		})
	}
	slices.SortFunc(notes, func(a, b model.CommentNotification) int {
		return a.PublishedAt.Compare(b.PublishedAt)
	})

	target.LastError = ""
	content, _, contentErr := store.Content(model.ContentID(model.PlatformBilibili, target.DynamicID))
	if errors.Is(contentErr, state.ErrNotFound) {
		externalID := target.DynamicID
		if externalID == "" {
			externalID = target.CommentOID
		}
		source := model.Source{ID: model.SourceID(model.PlatformBilibili, target.UID), Platform: model.PlatformBilibili,
			Type: model.SourceBilibiliUP, ExternalID: target.UID, Name: target.UPName, Enabled: true, BaselineState: model.BaselineComplete}
		if err := store.PutSource(source); err != nil {
			return err
		}
		content = model.Content{ID: model.ContentID(model.PlatformBilibili, externalID), Platform: model.PlatformBilibili,
			SourceID: source.ID, ExternalID: externalID, AuthorID: target.UID, AuthorName: target.UPName,
			UpstreamType: firstNonEmptyString(target.ContentType, "commentable"), Type: biliContentType(target.ContentType),
			Title: target.Title, URL: target.URL, PublishedAt: target.PublishedAt, LastSyncedAt: time.Now()}
		if content.PublishedAt.IsZero() {
			content.PublishedAt = time.Now()
		}
	} else if contentErr != nil {
		return contentErr
	}
	nodes := make([]model.CommentNode, 0, len(roots)+len(children))
	for _, reply := range roots {
		nodes = append(nodes, biliCommentNode(content.ID, target.UID, reply, true))
	}
	for _, reply := range children {
		nodes = append(nodes, biliCommentNode(content.ID, target.UID, reply, false))
	}
	slices.SortFunc(nodes, func(a, b model.CommentNode) int {
		if order := a.Time.Compare(b.Time); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	digests, err := store.SyncCommentTree(content, nodes, !paginationIncomplete, !target.BaselineReady, newBatchID("bilibili"), channelIDs)
	if err != nil {
		return err
	}
	if _, err := store.PromotePlatformOutbox(time.Now(), 50); err != nil {
		return err
	}
	// Preserve the list projection while the v3 tree is the authoritative archive.
	if _, err := store.RecordCommentNotifications(target, notes, nil, !target.BaselineReady); err != nil {
		return err
	}
	created := 0
	if len(digests) > 0 {
		created = len(digests[0].Triggers)
	}
	e.metrics.RecordCommentPoll(ctx, "success", created)
	if created > 0 {
		e.logger.InfoContext(ctx, "new UP replies queued", "event", "comment.replies_queued", "up_uid", target.UID, "comment_oid", target.CommentOID, "reply_count", created)
		e.publish(TopicStatus | TopicDeliveries | TopicComments | TopicContents)
	} else if len(notes) > 0 {
		e.publish(TopicComments | TopicContents)
	}
	return nil
}

func replyPageSignature(replies []bilibili.Reply) string {
	ids := make([]string, 0, len(replies))
	for _, reply := range replies {
		ids = append(ids, reply.RPID)
	}
	slices.Sort(ids)
	return strings.Join(ids, "\x00")
}

func biliCommentNode(contentID, upUID string, reply bilibili.Reply, root bool) model.CommentNode {
	id := model.CommentID(model.PlatformBilibili, reply.RPID)
	rootID := model.CommentID(model.PlatformBilibili, reply.Root)
	parentID := model.CommentID(model.PlatformBilibili, reply.Parent)
	if root || reply.Root == "" || reply.Root == "0" {
		rootID, parentID = id, ""
	} else if reply.Parent == "" || reply.Parent == "0" {
		parentID = rootID
	}
	role := model.RoleMember
	if reply.Mid == upUID {
		role = model.RoleUP
	}
	return model.CommentNode{ID: id, Platform: model.PlatformBilibili, ContentID: contentID, RootID: rootID, ParentID: parentID,
		RPID: reply.RPID, Parent: strings.TrimPrefix(parentID, "bilibili:comment:"), AuthorID: reply.Mid, Mid: reply.Mid,
		Name: reply.Name, Role: role, Message: reply.Message, Time: reply.CTime, IsUP: role == model.RoleUP}
}

func biliContentType(upstream string) model.ContentType {
	upper := strings.ToUpper(upstream)
	switch {
	case strings.Contains(upper, "AV") || strings.Contains(upper, "VIDEO"):
		return model.ContentVideo
	case strings.Contains(upper, "ARTICLE"):
		return model.ContentArticle
	default:
		return model.ContentDynamic
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func newBatchID(platform string) string {
	return fmt.Sprintf("%s:%d", platform, time.Now().UnixNano())
}

func buildCommentThread(target model.CommentTarget, trigger bilibili.Reply, roots, children map[string]bilibili.Reply) ([]model.CommentNode, bool) {
	incomplete := false
	// Collect chain from trigger up to root via parent links.
	byID := make(map[string]bilibili.Reply, len(roots)+len(children)+1)
	for id, reply := range roots {
		byID[id] = reply
	}
	for id, reply := range children {
		byID[id] = reply
	}
	byID[trigger.RPID] = trigger

	chain := make([]bilibili.Reply, 0, 8)
	current := trigger
	seen := map[string]struct{}{current.RPID: {}}
	for {
		chain = append(chain, current)
		parentID := current.Parent
		if parentID == "" || parentID == "0" {
			// If this is not a root and we know root, ensure root is present.
			if current.Root != "" && current.Root != current.RPID {
				if root, ok := byID[current.Root]; ok {
					if _, exists := seen[root.RPID]; !exists {
						chain = append(chain, root)
					}
				} else {
					incomplete = true
				}
			}
			break
		}
		parent, ok := byID[parentID]
		if !ok {
			// Fall back to root if available.
			if current.Root != "" {
				if root, ok := byID[current.Root]; ok {
					if _, exists := seen[root.RPID]; !exists {
						chain = append(chain, root)
					}
				} else {
					incomplete = true
				}
			} else {
				incomplete = true
			}
			break
		}
		if _, exists := seen[parent.RPID]; exists {
			break
		}
		seen[parent.RPID] = struct{}{}
		current = parent
	}
	// chain is trigger -> ... -> root; reverse to root -> trigger.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	nodes := make([]model.CommentNode, 0, len(chain))
	for _, reply := range chain {
		nodes = append(nodes, model.CommentNode{
			RPID:      reply.RPID,
			Parent:    reply.Parent,
			Mid:       reply.Mid,
			Name:      reply.Name,
			Message:   reply.Message,
			Time:      reply.CTime,
			IsUP:      reply.Mid == target.UID,
			IsTrigger: reply.RPID == trigger.RPID,
		})
	}
	return nodes, incomplete
}

func (e *Engine) handleCommentPollError(ctx context.Context, target model.CommentTarget, pollErr error) error {
	store := e.store.WithContext(ctx)
	if bilibili.IsAuthentication(pollErr) {
		e.setAuth(false)
	}
	if bilibili.IsRiskControl(pollErr) {
		now := time.Now()
		pause := e.Settings().RiskPause()
		previousUntil := e.riskUntil.Swap(now.Add(pause).Unix())
		if previousUntil <= now.Unix() {
			e.publish(TopicStatus)
			e.enqueueSystem(fmt.Sprintf("B站接口触发风控，采集已暂停 %s；服务不会尝试绕过风控。", pause.Round(time.Second)))
		}
	}
	if bilibili.IsCommentClosed(pollErr) {
		target.Closed = true
		target.LastError = pollErr.Error()
		_ = store.UpdateCommentTarget(target)
		e.metrics.RecordCommentPoll(ctx, "closed", 0)
		e.logger.InfoContext(ctx, "comment area closed", "event", "comment.area_closed", "up_uid", target.UID, "comment_type", target.CommentType, "comment_oid", target.CommentOID)
		return nil
	}
	target.LastError = pollErr.Error()
	_ = store.UpdateCommentTarget(target)
	e.metrics.RecordCommentPoll(ctx, "error", 0)
	e.logger.WarnContext(ctx, "comment target poll failed", "event", "comment.target.poll_failed", "up_uid", target.UID, "comment_oid", target.CommentOID, "error", pollErr)
	return nil
}

func (e *Engine) failPoll(ctx context.Context, up model.UP, name string, started time.Time, pollErr error) error {
	store := e.store.WithContext(ctx)
	if name == "" {
		name = up.Name
	}
	e.metrics.RecordWorkflow(ctx, "up_poll", "error", time.Since(started))
	if err := store.SetUPResult(up.UID, name, time.Now(), pollErr); err != nil {
		return fmt.Errorf("recording failed poll for UP %s: %w", up.UID, err)
	}
	e.publish(TopicStatus | TopicUPs)
	kind := "other"
	var apiErr *bilibili.APIError
	if errors.As(pollErr, &apiErr) {
		kind = string(apiErr.Kind)
	}
	e.logger.WarnContext(ctx, "Bilibili UP poll failed", "event", "bilibili.up.poll_completed", "result", "failure", "up_uid", up.UID, "up_name", name, "error_kind", kind, "consecutive_failures", up.ConsecutiveFail+1, "duration_ms", elapsedMS(started), "error", pollErr)
	if up.ConsecutiveFail+1 == 3 {
		e.enqueueSystem(fmt.Sprintf("UP %s 已连续三次采集失败：%v", up.UID, pollErr))
	}
	return nil
}

func (e *Engine) deliveryLoop(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := e.dispatchOnce(ctx); err != nil {
				e.logger.Error("delivery cycle failed", "event", "delivery.cycle.failed", "error", err)
			}
		}
	}
}

func (e *Engine) dispatchOnce(ctx context.Context) (err error) {
	// Delivery loop ticks every second. Idle ticks must not create root traces —
	// they dominated Tempo with empty delivery.dispatch + SQLite select spans.
	started := time.Now()
	store := e.store.WithContext(ctx)
	if _, err := store.PromotePlatformOutbox(time.Now(), 50); err != nil {
		e.metrics.RecordWorkflow(ctx, "delivery", "error", time.Since(started))
		return err
	}
	deliveries, err := store.DueDeliveries(time.Now(), 50)
	if err != nil {
		e.metrics.RecordWorkflow(ctx, "delivery", "error", time.Since(started))
		return err
	}
	all, listErr := store.ListDeliveries(0)
	if listErr == nil {
		var age time.Duration
		if len(all) > 0 {
			age = time.Since(oldestDelivery(all))
		}
		e.metrics.SetOutbox(all, age)
		settings := e.Settings()
		backlogged := len(all) > settings.BacklogAlertCount || age > settings.BacklogAlertAge()
		if backlogged && e.backlogAlerted.CompareAndSwap(false, true) {
			e.enqueueSystem(fmt.Sprintf("通知队列发生积压：任务数 %d，最老任务等待 %s。", len(all), age.Round(time.Second)))
		}
		if !backlogged && e.backlogAlerted.CompareAndSwap(true, false) {
			e.enqueueSystem("通知队列积压已恢复。")
		}
	}
	if len(deliveries) == 0 {
		e.metrics.RecordWorkflow(ctx, "delivery", "success", time.Since(started))
		return nil
	}

	ctx, span := e.tracer.Start(ctx, "delivery.dispatch",
		trace.WithAttributes(attribute.Int("delivery.count", len(deliveries))))
	defer func() {
		result := "success"
		if err != nil {
			result = "error"
		}
		e.metrics.RecordWorkflow(ctx, "delivery", result, time.Since(started))
		finishSpan(span, err)
	}()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(e.Settings().DeliveryConcurrency)
	var changed atomic.Bool
	for _, delivery := range deliveries {
		g.Go(func() error {
			deliveryChanged, err := e.deliver(gctx, delivery)
			if deliveryChanged {
				changed.Store(true)
			}
			return err
		})
	}
	err = g.Wait()
	if changed.Load() {
		e.publish(TopicStatus | TopicDeliveries)
	}
	return err
}

func (e *Engine) deliver(ctx context.Context, delivery model.Delivery) (changed bool, err error) {
	if originCtx, ok := deliveryOriginContext(ctx, delivery.OriginTraceparent); ok {
		ctx = originCtx
	}
	ctx, span := e.tracer.Start(ctx, "delivery.send", trace.WithAttributes(
		attribute.String("delivery.kind", string(delivery.EffectiveKind())),
		attribute.String("delivery.id", delivery.ID),
		attribute.Int("delivery.attempt", delivery.Attempts+1),
	))
	defer func() { finishSpan(span, err) }()
	store := e.store.WithContext(ctx)
	channel, err := store.Channel(delivery.ChannelID)
	if err != nil {
		deliveryErr := errors.New("channel no longer exists")
		if err := store.FailDelivery(delivery.ID, true, time.Now(), deliveryErr, delivery.Progress); err != nil {
			return false, err
		}
		e.logger.WarnContext(ctx, "notification delivery blocked", "event", "delivery.blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", delivery.ChannelID, "attempt", delivery.Attempts+1, "error", deliveryErr)
		return true, nil
	}
	if !channel.Enabled {
		return false, nil
	}
	if channel.Type == model.ChannelMicrosoft {
		e.microsoftSendMu.Lock()
		defer e.microsoftSendMu.Unlock()
		channel, err = store.Channel(delivery.ChannelID)
		if err != nil {
			deliveryErr := errors.New("channel no longer exists")
			if err := store.FailDelivery(delivery.ID, true, time.Now(), deliveryErr, delivery.Progress); err != nil {
				return false, err
			}
			e.logger.WarnContext(ctx, "notification delivery blocked", "event", "delivery.blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", delivery.ChannelID, "attempt", delivery.Attempts+1, "error", deliveryErr)
			return true, nil
		}
	}
	sender, err := e.newSender(channel)
	if err != nil {
		if storeErr := store.FailDelivery(delivery.ID, true, time.Now(), err, delivery.Progress); storeErr != nil {
			return false, storeErr
		}
		e.logger.WarnContext(ctx, "notification delivery blocked", "event", "delivery.blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "error", err)
		return true, nil
	}
	message, contentID, err := deliveryMessage(delivery)
	if err != nil {
		if storeErr := store.FailDelivery(delivery.ID, true, time.Now(), err, delivery.Progress); storeErr != nil {
			return false, storeErr
		}
		e.logger.WarnContext(ctx, "notification delivery blocked", "event", "delivery.blocked", "delivery_id", delivery.ID, "content_id", contentID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "error", err)
		return true, nil
	}
	timeout := 12 * time.Second
	if messageHasLocalImages(message) {
		timeout = 60 * time.Second
	}
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	sendCtx, sendSpan := e.tracer.Start(sendCtx, "notification.send",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("notification.channel.type", string(channel.Type))),
	)
	started := time.Now()
	progress := delivery.Progress
	if progressive, ok := sender.(notify.ProgressiveSender); ok {
		progress, err = progressive.SendProgressive(sendCtx, message, delivery.Progress)
	} else {
		err = sender.Send(sendCtx, message)
	}
	finishSpan(sendSpan, err)
	cancel()
	if err == nil {
		e.metrics.RecordDelivery(ctx, string(channel.Type), "success", time.Since(started))
		if err := store.CompleteDelivery(delivery.ID); err != nil {
			return false, err
		}
		e.logger.InfoContext(ctx, "notification delivered", "event", "delivery.completed", "result", "success", "delivery_id", delivery.ID, "content_id", contentID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "duration_ms", elapsedMS(started))
		return true, nil
	}
	blocked := notify.IsPermanent(err)
	result := "retry"
	if blocked {
		result = "blocked"
	}
	e.metrics.RecordDelivery(ctx, string(channel.Type), result, time.Since(started))
	next := nextDeliveryRetry(time.Now(), delivery.Attempts, e.Settings().DeliveryRetryDelaysSec, err)
	if storeErr := store.FailDelivery(delivery.ID, blocked, next, err, progress); storeErr != nil {
		return false, storeErr
	}
	e.logger.WarnContext(ctx, "notification delivery failed", "event", "delivery.completed", "delivery_id", delivery.ID, "content_id", contentID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "result", result, "next_attempt_at", next, "duration_ms", elapsedMS(started), "error", err)
	return true, nil
}

func deliveryMessage(delivery model.Delivery) (notify.Message, string, error) {
	switch delivery.EffectiveKind() {
	case model.DeliveryKindComment:
		if delivery.Comment == nil {
			return notify.Message{}, "", errors.New("comment delivery is missing payload")
		}
		return notify.CommentThreadMessage(*delivery.Comment), delivery.Comment.RPID, nil
	case model.DeliveryKindAI:
		if delivery.AI == nil {
			return notify.Message{}, "", errors.New("AI delivery is missing payload")
		}
		return notify.AINotificationMessage(*delivery.AI), delivery.AI.JobID, nil
	default:
		return notify.DynamicMessage(delivery.Dynamic), delivery.Dynamic.ID, nil
	}
}

func retryDelay(attempt int, delays model.DeliveryRetryDelays) time.Duration {
	base := time.Duration(delays[min(attempt, len(delays)-1)]) * time.Second
	return base/2 + rand.N(base/2)
}

func nextDeliveryRetry(now time.Time, attempt int, delays model.DeliveryRetryDelays, sendErr error) time.Time {
	delay := retryDelay(attempt, delays)
	if upstreamDelay, ok := notify.RetryAfter(sendErr); ok && upstreamDelay > delay {
		delay = upstreamDelay
	}
	return now.Add(delay)
}

func elapsedMS(started time.Time) int64 {
	return time.Since(started).Milliseconds()
}

func (e *Engine) authLoop(ctx context.Context) error {
	ticker := time.NewTicker(sessionValidationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !e.authValid.Load() {
				continue
			}
			checkCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
			e.sessionMu.RLock()
			_, err := e.client.ValidateSession(checkCtx)
			e.sessionMu.RUnlock()
			cancel()
			if err != nil {
				e.setAuth(false)
				e.logger.Warn("Bilibili session validation failed", "event", "bilibili.session.validation_failed", "error", err)
			}
		}
	}
}

func (e *Engine) setAccount(account model.BiliAccount) {
	e.accountMu.Lock()
	e.account = account
	e.accountMu.Unlock()
}

func (e *Engine) currentAccount() model.BiliAccount {
	e.accountMu.RLock()
	defer e.accountMu.RUnlock()
	return e.account
}

func (e *Engine) NotifyUPChanged() {
	select {
	case e.relationNotify <- struct{}{}:
	default:
	}
}

func (e *Engine) setAuth(valid bool) {
	previous := e.authValid.Swap(valid)
	e.metrics.SetAuth(valid)
	if valid {
		if !previous {
			e.logger.Info("Bilibili authentication state changed", "event", "bilibili.authentication.changed", "authenticated", true)
		}
		wasEverValid := e.authEverValid.Swap(true)
		if !previous && wasEverValid {
			e.enqueueSystem("B站登录已恢复，动态采集重新开始。")
		}
	} else {
		if previous {
			e.logger.Warn("Bilibili authentication state changed", "event", "bilibili.authentication.changed", "authenticated", false)
		}
		if previous && e.authEverValid.Load() {
			e.enqueueSystem("B站登录失效，请在管理控制台重新扫码登录。")
		}
		if previous {
			if err := e.store.SetPlatformAccountStatus(model.PlatformBilibili, model.AccountInvalid, "session validation failed"); err != nil {
				e.logger.Error("unable to persist Bilibili authentication state", "event", "bilibili.authentication.persist_failed", "error", err)
			}
		}
	}
	if previous != valid {
		e.publish(TopicStatus)
	}
}

func (e *Engine) enqueueSystem(summary string) {
	channels, err := e.store.ListChannels()
	if err != nil {
		e.logger.Error("unable to list channels for system alert", "event", "system_alert.queue_failed", "phase", "list_channels", "error", err)
		return
	}
	ids := enabledChannelIDs(channels)
	if len(ids) == 0 {
		return
	}
	now := time.Now()
	dynamic := model.Dynamic{
		ID: fmt.Sprintf("system:%d", now.UnixNano()), UID: "system", UPName: "Bili Notify", Type: "SYSTEM",
		PublishedAt: now, Summary: summary, URL: "",
	}
	if _, err := e.store.RecordDynamics("system", []model.Dynamic{dynamic}, ids, state.DynamicBaselineNone); err != nil {
		e.logger.Error("unable to queue system alert", "event", "system_alert.queue_failed", "phase", "record_delivery", "error", err)
		return
	}
	e.publish(TopicStatus | TopicDeliveries)
}

func (e *Engine) StartLogin(ctx context.Context) (LoginSession, error) {
	login, err := e.client.GenerateQR(ctx)
	if err != nil {
		return LoginSession{}, err
	}
	session := LoginSession{Key: login.Key, URL: login.URL, Status: bilibili.QRWaiting, ExpiresAt: time.Now().Add(3 * time.Minute)}
	e.loginMu.Lock()
	if e.runCtx == nil {
		e.loginMu.Unlock()
		return LoginSession{}, errors.New("notification engine is not running")
	}
	if e.loginCancel != nil {
		e.loginCancel()
	}
	loginCtx, cancel := context.WithCancel(e.runCtx)
	loginKey := session.Key
	e.login = &session
	e.loginCancel = cancel
	e.loginWG.Add(1)
	e.loginMu.Unlock()
	e.logger.Info("Bilibili QR login started", "event", "bilibili.login.started", "expires_at", session.ExpiresAt)
	e.publish(TopicBiliLogin)
	go func() {
		defer e.loginWG.Done()
		e.pollLoginLoop(loginCtx, loginKey)
	}()
	return session, nil
}

func (e *Engine) pollLoginLoop(ctx context.Context, id string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			previous, _ := e.Login()
			pollCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
			login, err := e.PollLogin(pollCtx, id)
			cancel()
			if err != nil {
				e.logger.Warn("Bilibili QR login poll failed", "event", "bilibili.login.poll_failed", "error", err)
				continue
			}
			if login.Status != previous.Status {
				e.publish(TopicBiliLogin)
			}
			if login.Status == bilibili.QRSuccess || login.Status == bilibili.QRExpired {
				return
			}
		}
	}
}

func (e *Engine) Login() (LoginSession, bool) {
	e.loginMu.Lock()
	defer e.loginMu.Unlock()
	if e.login == nil {
		return LoginSession{}, false
	}
	return *e.login, true
}

func (e *Engine) LoginURL(id string) (string, error) {
	e.loginMu.Lock()
	defer e.loginMu.Unlock()
	if e.login == nil || e.login.Key != id {
		return "", errors.New("login session not found")
	}
	return e.login.URL, nil
}

func (e *Engine) PollLogin(ctx context.Context, id string) (LoginSession, error) {
	e.loginMu.Lock()
	if e.login == nil || e.login.Key != id {
		e.loginMu.Unlock()
		return LoginSession{}, errors.New("login session not found")
	}
	current := *e.login
	e.loginMu.Unlock()
	if time.Now().After(current.ExpiresAt) {
		current.Status = bilibili.QRExpired
		e.loginMu.Lock()
		if e.login != nil && e.login.Key == id {
			*e.login = current
		}
		e.loginMu.Unlock()
		return current, nil
	}
	status, session, err := e.client.PollQR(ctx, id)
	if err != nil {
		return LoginSession{}, err
	}
	previousStatus := current.Status
	current.Status = status
	if previousStatus != status {
		e.logger.Info("Bilibili QR login status changed", "event", "bilibili.login.status_changed", "status", status)
	}
	if status == bilibili.QRSuccess {
		e.sessionMu.Lock()
		defer e.sessionMu.Unlock()
		previous, previousErr := e.store.Session()
		if previousErr != nil && !errors.Is(previousErr, state.ErrNotFound) {
			return LoginSession{}, previousErr
		}
		restorePrevious := func() {
			if previousErr == nil {
				e.client.SetSession(previous)
				return
			}
			e.client.ClearSession()
		}
		e.client.SetSession(session)
		account, err := e.client.ValidateSession(ctx)
		if err != nil {
			restorePrevious()
			return LoginSession{}, fmt.Errorf("validating new session: %w", err)
		}
		session.AccountUID = account.UID
		session.AccountName = account.Name
		if err := e.store.SaveSession(session); err != nil {
			restorePrevious()
			return LoginSession{}, err
		}
		accountChanged := previous.AccountUID != account.UID
		identityChanged := accountChanged || previous.AccountName != account.Name
		if accountChanged {
			e.lastSuccess.Store(0)
		}
		e.setAccount(account)
		e.setAuth(true)
		if identityChanged {
			e.publish(TopicStatus | TopicUPs)
		}
		e.NotifyUPChanged()
		e.logger.Info("Bilibili QR login completed", "event", "bilibili.login.completed", "result", "success")
	}
	e.loginMu.Lock()
	if e.login != nil && e.login.Key == id {
		*e.login = current
	}
	e.loginMu.Unlock()
	return current, nil
}

func (e *Engine) CancelLogin(id string) {
	e.loginMu.Lock()
	changed := false
	if e.login != nil && e.login.Key == id {
		if e.loginCancel != nil {
			e.loginCancel()
		}
		e.loginCancel = nil
		e.login = nil
		changed = true
	}
	e.loginMu.Unlock()
	if changed {
		e.publish(TopicBiliLogin)
	}
}

func (e *Engine) TestChannel(ctx context.Context, id string) error {
	channel, err := e.store.Channel(id)
	if err != nil {
		return err
	}
	if channel.Type == model.ChannelMicrosoft {
		e.microsoftSendMu.Lock()
		defer e.microsoftSendMu.Unlock()
		channel, err = e.store.Channel(id)
		if err != nil {
			return err
		}
	}
	sender, err := e.newSender(channel)
	if err != nil {
		return err
	}
	started := time.Now()
	if err := sender.Send(ctx, notify.TextMessage("Bili Notify 测试", "Bili Notify 通知渠道配置成功。")); err != nil {
		e.logger.Warn("notification channel test failed", "event", "channel.test.completed", "result", "failure", "channel_id", channel.ID, "channel_type", channel.Type, "duration_ms", elapsedMS(started), "error", err)
		return err
	}
	e.logger.Info("notification channel test succeeded", "event", "channel.test.completed", "result", "success", "channel_id", channel.ID, "channel_type", channel.Type, "duration_ms", elapsedMS(started))
	return nil
}

func (e *Engine) newSender(channel model.Channel) (notify.Sender, error) {
	dataDir := ""
	if e.media != nil {
		dataDir = e.media.DataDir
	}
	return notify.NewSender(channel, e.notificationClient, dataDir, func(settings map[string]string) error {
		_, err := e.store.UpdateChannelSettings(channel.ID, settings)
		if err == nil {
			e.publish(TopicChannels)
		}
		return err
	})
}

func (e *Engine) StartMicrosoftLogin(ctx context.Context, channelID string) (MicrosoftLoginSession, error) {
	channel, err := e.store.Channel(channelID)
	if err != nil {
		return MicrosoftLoginSession{}, err
	}
	if channel.Type != model.ChannelMicrosoft {
		return MicrosoftLoginSession{}, errors.New("channel is not a Microsoft channel")
	}
	e.microsoftLoginMu.Lock()
	runCtx := e.microsoftRunCtx
	e.microsoftLoginMu.Unlock()
	if runCtx == nil {
		return MicrosoftLoginSession{}, errors.New("notification engine is not running")
	}
	auth, err := notify.StartMicrosoftDeviceAuth(ctx, channel.Settings, e.notificationClient)
	if err != nil {
		return MicrosoftLoginSession{}, err
	}
	e.microsoftLoginMu.Lock()
	runCtx = e.microsoftRunCtx
	if runCtx == nil {
		e.microsoftLoginMu.Unlock()
		return MicrosoftLoginSession{}, errors.New("notification engine stopped while starting Microsoft authorization")
	}
	loginCtx, cancel := context.WithCancel(runCtx)
	session := &MicrosoftLoginSession{
		ChannelID: channelID, Status: "pending", UserCode: auth.UserCode,
		VerificationURI: auth.VerificationURI, VerificationURIComplete: auth.VerificationURIComplete,
		ExpiresAt: auth.ExpiresAt, auth: auth, cancel: cancel,
	}
	if previous := e.microsoftLogins[channelID]; previous != nil && previous.cancel != nil {
		previous.cancel()
	}
	e.microsoftLogins[channelID] = session
	e.microsoftLoginWG.Add(1)
	e.microsoftLoginMu.Unlock()
	public := publicMicrosoftLogin(session)
	e.logger.Info("Microsoft authorization started", "event", "microsoft.authorization.started", "channel_id", channelID, "tenant", channel.Settings["tenant"], "expires_at", session.ExpiresAt)
	e.publish(TopicMicrosoftLogin)
	go func() {
		defer e.microsoftLoginWG.Done()
		e.completeMicrosoftLogin(loginCtx, session)
	}()
	return public, nil
}

func (e *Engine) completeMicrosoftLogin(ctx context.Context, session *MicrosoftLoginSession) {
	defer e.publish(TopicMicrosoftLogin | TopicChannels | TopicStatus | TopicDeliveries)
	settings, err := session.auth.Exchange(ctx, e.notificationClient)
	if err == nil {
		e.microsoftSendMu.Lock()
		_, err = e.store.UpdateChannelSettings(session.ChannelID, settings)
		if err == nil {
			err = e.store.UnblockChannel(session.ChannelID)
		}
		e.microsoftSendMu.Unlock()
	}
	e.microsoftLoginMu.Lock()
	defer e.microsoftLoginMu.Unlock()
	if e.microsoftLogins[session.ChannelID] != session {
		return
	}
	if err == nil {
		session.Status = "success"
		session.Error = ""
		e.logger.Info("Microsoft authorization completed", "event", "microsoft.authorization.completed", "result", "success", "channel_id", session.ChannelID)
		return
	}
	if errors.Is(err, context.Canceled) {
		session.Status = "canceled"
		e.logger.Info("Microsoft authorization canceled", "event", "microsoft.authorization.completed", "result", "canceled", "channel_id", session.ChannelID)
		return
	}
	if !session.ExpiresAt.IsZero() && time.Now().After(session.ExpiresAt) {
		session.Status = "expired"
	} else {
		session.Status = "failed"
	}
	session.Error = err.Error()
	e.logger.Warn("Microsoft authorization failed", "event", "microsoft.authorization.completed", "result", "failure", "channel_id", session.ChannelID, "status", session.Status, "error", err)
}

func (e *Engine) MicrosoftLogin(channelID string) (MicrosoftLoginSession, error) {
	e.microsoftLoginMu.Lock()
	defer e.microsoftLoginMu.Unlock()
	session := e.microsoftLogins[channelID]
	if session == nil {
		return MicrosoftLoginSession{}, ErrMicrosoftLoginNotFound
	}
	return publicMicrosoftLogin(session), nil
}

func (e *Engine) CancelMicrosoftLogin(channelID string) {
	e.microsoftLoginMu.Lock()
	changed := false
	if session := e.microsoftLogins[channelID]; session != nil {
		if session.cancel != nil {
			session.cancel()
		}
		if session.Status != "canceled" {
			session.Status = "canceled"
			changed = true
		}
	}
	e.microsoftLoginMu.Unlock()
	if changed {
		e.publish(TopicMicrosoftLogin)
	}
}

func (e *Engine) MicrosoftLogins() []MicrosoftLoginSession {
	e.microsoftLoginMu.Lock()
	defer e.microsoftLoginMu.Unlock()
	logins := make([]MicrosoftLoginSession, 0, len(e.microsoftLogins))
	for _, session := range e.microsoftLogins {
		logins = append(logins, publicMicrosoftLogin(session))
	}
	slices.SortFunc(logins, func(a, b MicrosoftLoginSession) int { return cmp.Compare(a.ChannelID, b.ChannelID) })
	return logins
}

func (e *Engine) publish(topics Topic) {
	e.events.Publish(topics)
}

func (e *Engine) publishClockStatusIfChanged() error {
	status, err := e.Status()
	if err != nil {
		return err
	}
	next := clockStatus{Ready: status.Ready}
	if !status.RiskPausedUntil.IsZero() {
		next.RiskPausedUntil = status.RiskPausedUntil.Unix()
	}
	e.clockStatusMu.Lock()
	changed := !e.clockStatusKnown || e.clockStatus != next
	e.clockStatus = next
	e.clockStatusKnown = true
	e.clockStatusMu.Unlock()
	if changed {
		e.publish(TopicStatus)
	}
	return nil
}

func publicMicrosoftLogin(session *MicrosoftLoginSession) MicrosoftLoginSession {
	return MicrosoftLoginSession{
		ChannelID: session.ChannelID, Status: session.Status, UserCode: session.UserCode,
		VerificationURI: session.VerificationURI, VerificationURIComplete: session.VerificationURIComplete,
		ExpiresAt: session.ExpiresAt, Error: session.Error,
	}
}

func (e *Engine) Status() (Status, error) {
	ups, err := e.store.ListUPs()
	if err != nil {
		return Status{}, err
	}
	channels, err := e.store.ListChannels()
	if err != nil {
		return Status{}, err
	}
	deliveries, err := e.store.ListDeliveries(0)
	if err != nil {
		return Status{}, err
	}
	commentTargets, err := e.store.ListAllCommentTargets()
	if err != nil {
		return Status{}, err
	}
	sources, err := e.store.ListSources("")
	if err != nil {
		return Status{}, err
	}
	accounts, err := e.store.ListPlatformAccounts()
	if err != nil {
		return Status{}, err
	}
	status := Status{AuthValid: e.authValid.Load(), UPCount: len(ups), ChannelCount: len(channels), OutboxDepth: len(deliveries)}
	if account := e.currentAccount(); account.UID != "" {
		status.BiliAccount = &account
	}
	if unix := e.lastSuccess.Load(); unix > 0 {
		status.LastSuccessAt = time.Unix(unix, 0)
	}
	if len(deliveries) > 0 {
		status.OldestDelivery = oldestDelivery(deliveries)
	}
	enabledSources := 0
	platformReady := map[model.Platform]bool{model.PlatformBilibili: status.AuthValid}
	for _, account := range accounts {
		if account.Platform == model.PlatformZSXQ {
			platformReady[account.Platform] = account.Status == model.AccountConnected && (account.RiskPausedUntil.IsZero() || time.Now().After(account.RiskPausedUntil))
		}
	}
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		enabledSources++
		if !platformReady[source.Platform] {
			continue
		}
		if source.LastSuccessAt.After(status.LastSuccessAt) {
			status.LastSuccessAt = source.LastSuccessAt
		}
	}
	status.Ready = enabledSources > 0 && len(enabledChannelIDs(channels)) > 0
	now := time.Now()
	settings := e.Settings()
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		if !platformReady[source.Platform] {
			status.Ready = false
			continue
		}
		interval := settings.PollInterval()
		if source.Platform == model.PlatformZSXQ {
			interval = time.Duration(settings.ZSXQDynamicIntervalSec) * time.Second
		}
		if source.LastSuccessAt.IsZero() || now.Sub(source.LastSuccessAt) > max(2*time.Minute, 2*interval) {
			status.Ready = false
		}
	}
	if until := e.riskUntil.Load(); until > now.Unix() {
		status.RiskPausedUntil = time.Unix(until, 0)
		for _, source := range sources {
			if source.Enabled && source.Platform == model.PlatformBilibili {
				status.Ready = false
			}
		}
	}
	e.metrics.SetStatus(status.Ready, !status.RiskPausedUntil.IsZero(), len(ups), enabledUPCount(ups), len(channels), len(enabledChannelIDs(channels)), len(commentTargets))
	return status, nil
}

func oldestDelivery(deliveries []model.Delivery) time.Time {
	oldest := deliveries[0].CreatedAt
	for _, delivery := range deliveries[1:] {
		if delivery.CreatedAt.Before(oldest) {
			oldest = delivery.CreatedAt
		}
	}
	return oldest
}

func enabledChannelIDs(channels []model.Channel) []string {
	ids := make([]string, 0, len(channels))
	for _, channel := range channels {
		if channel.Enabled {
			ids = append(ids, channel.ID)
		}
	}
	return ids
}

func enabledUPCount(ups []model.UP) int {
	count := 0
	for _, up := range ups {
		if up.Enabled {
			count++
		}
	}
	return count
}

func (e *Engine) enrichMedia(ctx context.Context, items []model.Dynamic) {
	if e.media == nil || len(items) == 0 {
		return
	}
	for i := range items {
		ok, bad, downloadedBytes := e.media.Ensure(ctx, &items[i])
		e.metrics.RecordMediaDownloads(ctx, ok, bad)
		e.metrics.AddMediaBytes(ctx, downloadedBytes)
		if bad > 0 {
			e.logger.WarnContext(ctx, "media download incomplete", "event", "media.download.incomplete", "dynamic_id", items[i].ID, "up_uid", items[i].UID, "downloaded", ok, "failed", bad)
		}
	}
}

func messageHasLocalImages(message notify.Message) bool {
	for _, section := range message.Sections {
		for _, image := range section.Images {
			if image.LocalPath != "" {
				return true
			}
		}
	}
	return false
}

func finishSpan(span trace.Span, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		span.SetStatus(codes.Error, "operation failed")
	}
	span.End()
}

// deliveryOriginContext restores the producer span saved with an outbox row.
// Extraction starts from an empty context so an invalid header cannot silently
// fall back to the dispatch span and be mistaken for a valid origin.
func deliveryOriginContext(ctx context.Context, traceparent string) (context.Context, bool) {
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	extracted := propagation.TraceContext{}.Extract(context.Background(), carrier)
	spanContext := trace.SpanContextFromContext(extracted)
	if !spanContext.IsValid() {
		return ctx, false
	}
	return trace.ContextWithRemoteSpanContext(ctx, spanContext), true
}
