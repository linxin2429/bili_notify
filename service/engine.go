package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/notify"
	"github.com/linxin2429/bili_notify/state"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

type Engine struct {
	store            *state.Store
	client           *bilibili.Client
	logger           *slog.Logger
	metrics          *Metrics
	pollInterval     time.Duration
	limiter          *rate.Limiter
	concurrency      int
	httpTimeout      time.Duration
	authValid        atomic.Bool
	authEverValid    atomic.Bool
	lastSuccess      atomic.Int64
	riskUntil        atomic.Int64
	backlogAlerted   atomic.Bool
	loginMu          sync.Mutex
	login            *LoginSession
	loginCancel      context.CancelFunc
	loginWG          sync.WaitGroup
	runCtx           context.Context
	microsoftSendMu  sync.Mutex
	microsoftLoginMu sync.Mutex
	microsoftLoginWG sync.WaitGroup
	microsoftRunCtx  context.Context
	microsoftLogins  map[string]*MicrosoftLoginSession
	events           *EventBus
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
	AuthValid       bool      `json:"auth_valid"`
	LastSuccessAt   time.Time `json:"last_success_at,omitzero"`
	UPCount         int       `json:"up_count"`
	ChannelCount    int       `json:"channel_count"`
	OutboxDepth     int       `json:"outbox_depth"`
	OldestDelivery  time.Time `json:"oldest_delivery,omitzero"`
	Ready           bool      `json:"ready"`
	RiskPausedUntil time.Time `json:"risk_paused_until,omitzero"`
}

func NewEngine(store *state.Store, client *bilibili.Client, logger *slog.Logger, metrics *Metrics, pollInterval time.Duration, requestsPerSecond float64, concurrency int, events *EventBus) *Engine {
	if events == nil {
		events = NewEventBus()
	}
	return &Engine{
		store: store, client: client, logger: logger, metrics: metrics,
		pollInterval:    pollInterval,
		limiter:         rate.NewLimiter(rate.Limit(requestsPerSecond), max(1, int(requestsPerSecond))),
		concurrency:     concurrency,
		httpTimeout:     10 * time.Second,
		microsoftLogins: make(map[string]*MicrosoftLoginSession),
		events:          events,
	}
}

func (e *Engine) Run(ctx context.Context) error {
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
	if session, err := e.store.Session(); err == nil {
		e.client.SetSession(session)
		validateCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
		_, validateErr := e.client.ValidateSession(validateCtx)
		cancel()
		if validateErr == nil {
			e.authEverValid.Store(true)
			e.authValid.Store(true)
			e.metrics.AuthState.Set(1)
			e.logger.Info("stored Bilibili session restored")
		} else {
			e.authEverValid.Store(true)
			e.logger.Warn("stored Bilibili session is invalid", "err", validateErr)
			e.enqueueSystem("B站登录失效，请在管理控制台重新扫码登录。")
		}
	} else if !errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("loading Bilibili session: %w", err)
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.collectLoop(ctx) })
	g.Go(func() error { return e.deliveryLoop(ctx) })
	g.Go(func() error { return e.authLoop(ctx) })
	return g.Wait()
}

func (e *Engine) collectLoop(ctx context.Context) error {
	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()
	if err := e.collectOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		e.logger.Error("initial collection cycle failed", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := e.collectOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				e.logger.Error("collection cycle failed", "err", err)
			}
		}
	}
}

func (e *Engine) collectOnce(ctx context.Context) error {
	defer e.publish(TopicStatus | TopicUPs | TopicDeliveries)
	started := time.Now()
	if !e.authValid.Load() {
		e.logger.Debug("collection cycle skipped", "reason", "Bilibili session is not authenticated")
		return nil
	}
	if until := e.riskUntil.Load(); until > time.Now().Unix() {
		e.logger.Debug("collection cycle skipped", "reason", "Bilibili risk-control pause", "resume_at", time.Unix(until, 0).UTC())
		return nil
	}
	ups, err := e.store.ListUPs()
	if err != nil {
		return fmt.Errorf("listing UPs: %w", err)
	}
	channels, err := e.store.ListChannels()
	if err != nil {
		return fmt.Errorf("listing channels: %w", err)
	}
	channelIDs := enabledChannelIDs(channels)
	if len(channelIDs) == 0 {
		e.logger.Debug("collection cycle skipped", "reason", "no enabled notification channels")
		return nil
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(e.concurrency)
	enabledUPs := 0
	for _, up := range ups {
		if !up.Enabled {
			continue
		}
		enabledUPs++
		g.Go(func() error {
			if err := e.limiter.Wait(gctx); err != nil {
				return err
			}
			return e.pollUP(gctx, up, channelIDs)
		})
	}
	err = g.Wait()
	if err != nil {
		return err
	}
	e.logger.Info("collection cycle completed", "enabled_ups", enabledUPs, "enabled_channels", len(channelIDs), "duration", elapsed(started))
	return nil
}

func (e *Engine) pollUP(ctx context.Context, up model.UP, channelIDs []string) error {
	started := time.Now()
	defer func() { e.metrics.PollDuration.Observe(time.Since(started).Seconds()) }()
	requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
	defer cancel()
	var (
		items  []model.Dynamic
		offset string
		name   string
	)
	for pageNumber := range 10 {
		page, err := e.client.FetchPage(requestCtx, up.UID, offset)
		if err != nil {
			if bilibili.IsAuthentication(err) {
				e.setAuth(false)
			}
			if bilibili.IsRiskControl(err) {
				now := time.Now()
				previousUntil := e.riskUntil.Swap(now.Add(5 * time.Minute).Unix())
				if previousUntil <= now.Unix() {
					e.enqueueSystem("B站接口触发风控，采集已暂停五分钟；服务不会尝试绕过风控。")
				}
			}
			return e.failPoll(up, name, started, err)
		}
		name = page.UPName
		foundSeen := false
		for _, dynamic := range page.Items {
			seen, err := e.store.Seen(up.UID, dynamic.ID)
			if err != nil {
				return fmt.Errorf("checking seen dynamic: %w", err)
			}
			if seen {
				foundSeen = true
				continue
			}
			items = append(items, dynamic)
		}
		if !up.BaselineReady || foundSeen || !page.HasMore {
			break
		}
		if pageNumber == 9 {
			err := errors.New("more than 10 pages of unseen dynamics; manual review required")
			return e.failPoll(up, name, started, err)
		}
		offset = page.Offset
	}
	slices.SortFunc(items, func(a, b model.Dynamic) int { return a.PublishedAt.Compare(b.PublishedAt) })
	created, err := e.store.RecordDynamics(up.UID, items, channelIDs, !up.BaselineReady)
	if err != nil {
		return fmt.Errorf("recording dynamics: %w", err)
	}
	now := time.Now().UTC()
	if err := e.store.SetUPResult(up.UID, name, now, nil); err != nil {
		return err
	}
	if up.ConsecutiveFail >= 3 {
		e.enqueueSystem(fmt.Sprintf("UP %s 的动态采集已恢复。", up.UID))
	}
	if up.ConsecutiveFail > 0 {
		e.logger.Info("Bilibili UP poll recovered", "uid", up.UID, "up_name", name, "previous_failures", up.ConsecutiveFail)
	}
	e.metrics.PollTotal.WithLabelValues("success").Inc()
	e.metrics.LastPollSuccess.Set(float64(now.Unix()))
	e.lastSuccess.Store(now.Unix())
	for _, dynamic := range items {
		if up.BaselineReady {
			e.metrics.DiscoveryDelay.Observe(max(0, now.Sub(dynamic.PublishedAt).Seconds()))
		}
	}
	if created > 0 {
		e.logger.Info("new dynamics queued", "uid", up.UID, "up_name", name, "dynamic_count", created, "channel_count", len(channelIDs))
	} else if !up.BaselineReady {
		e.logger.Info("Bilibili UP baseline established", "uid", up.UID, "up_name", name, "baseline_items", len(items), "duration", elapsed(started))
	}
	e.logger.Debug("Bilibili UP poll succeeded", "uid", up.UID, "up_name", name, "fetched_items", len(items), "queued_dynamics", created, "duration", elapsed(started))
	return nil
}

func (e *Engine) failPoll(up model.UP, name string, started time.Time, pollErr error) error {
	if name == "" {
		name = up.Name
	}
	e.metrics.PollTotal.WithLabelValues("error").Inc()
	if err := e.store.SetUPResult(up.UID, name, time.Now().UTC(), pollErr); err != nil {
		return fmt.Errorf("recording failed poll for UP %s: %w", up.UID, err)
	}
	kind := "other"
	var apiErr *bilibili.APIError
	if errors.As(pollErr, &apiErr) {
		kind = string(apiErr.Kind)
	}
	e.logger.Warn("Bilibili UP poll failed", "uid", up.UID, "up_name", name, "error_kind", kind, "consecutive_failures", up.ConsecutiveFail+1, "duration", elapsed(started), "err", pollErr)
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
				e.logger.Error("delivery cycle failed", "err", err)
			}
		}
	}
}

func (e *Engine) dispatchOnce(ctx context.Context) error {
	defer e.publish(TopicStatus | TopicChannels | TopicDeliveries)
	deliveries, err := e.store.DueDeliveries(time.Now().UTC(), 50)
	if err != nil {
		return err
	}
	all, err := e.store.ListDeliveries(0)
	if err == nil {
		e.metrics.OutboxDepth.Set(float64(len(all)))
		var age time.Duration
		if len(all) > 0 {
			age = time.Since(oldestDelivery(all))
			e.metrics.OldestOutboxAge.Set(max(0, age.Seconds()))
		} else {
			e.metrics.OldestOutboxAge.Set(0)
		}
		backlogged := len(all) > 100 || age > 5*time.Minute
		if backlogged && e.backlogAlerted.CompareAndSwap(false, true) {
			e.enqueueSystem(fmt.Sprintf("通知队列发生积压：任务数 %d，最老任务等待 %s。", len(all), age.Round(time.Second)))
		}
		if !backlogged && e.backlogAlerted.CompareAndSwap(true, false) {
			e.enqueueSystem("通知队列积压已恢复。")
		}
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, delivery := range deliveries {
		g.Go(func() error { return e.deliver(gctx, delivery) })
	}
	return g.Wait()
}

func (e *Engine) deliver(ctx context.Context, delivery model.Delivery) error {
	channel, err := e.store.Channel(delivery.ChannelID)
	if err != nil {
		deliveryErr := errors.New("channel no longer exists")
		if err := e.store.FailDelivery(delivery.ID, true, time.Now().UTC(), deliveryErr); err != nil {
			return err
		}
		e.logger.Warn("notification delivery blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", delivery.ChannelID, "attempt", delivery.Attempts+1, "err", deliveryErr)
		return nil
	}
	if !channel.Enabled {
		return nil
	}
	if channel.Type == model.ChannelMicrosoft {
		e.microsoftSendMu.Lock()
		defer e.microsoftSendMu.Unlock()
		channel, err = e.store.Channel(delivery.ChannelID)
		if err != nil {
			deliveryErr := errors.New("channel no longer exists")
			if err := e.store.FailDelivery(delivery.ID, true, time.Now().UTC(), deliveryErr); err != nil {
				return err
			}
			e.logger.Warn("notification delivery blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", delivery.ChannelID, "attempt", delivery.Attempts+1, "err", deliveryErr)
			return nil
		}
	}
	sender, err := e.newSender(channel)
	if err != nil {
		if storeErr := e.store.FailDelivery(delivery.ID, true, time.Now().UTC(), err); storeErr != nil {
			return storeErr
		}
		e.logger.Warn("notification delivery blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "err", err)
		return nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	started := time.Now()
	err = sender.Send(sendCtx, notify.DynamicMessage(delivery.Dynamic))
	cancel()
	e.metrics.DeliveryDuration.Observe(time.Since(started).Seconds())
	if err == nil {
		e.metrics.DeliveryTotal.WithLabelValues(string(channel.Type), "success").Inc()
		if err := e.store.CompleteDelivery(delivery.ID); err != nil {
			return err
		}
		e.logger.Info("notification delivered", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "duration", elapsed(started))
		return nil
	}
	blocked := notify.IsPermanent(err)
	result := "retry"
	if blocked {
		result = "blocked"
	}
	e.metrics.DeliveryTotal.WithLabelValues(string(channel.Type), result).Inc()
	next := time.Now().Add(retryDelay(delivery.Attempts))
	if storeErr := e.store.FailDelivery(delivery.ID, blocked, next, err); storeErr != nil {
		return storeErr
	}
	e.logger.Warn("notification delivery failed", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "result", result, "next_attempt_at", next.UTC(), "duration", elapsed(started), "err", err)
	return nil
}

func retryDelay(attempt int) time.Duration {
	delays := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour}
	base := delays[min(attempt, len(delays)-1)]
	return base/2 + rand.N(base/2)
}

func elapsed(started time.Time) string {
	return time.Since(started).Round(time.Millisecond).String()
}

func (e *Engine) authLoop(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Minute)
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
			_, err := e.client.ValidateSession(checkCtx)
			cancel()
			if err != nil {
				e.setAuth(false)
				e.logger.Warn("Bilibili session validation failed", "err", err)
			}
		}
	}
}

func (e *Engine) setAuth(valid bool) {
	defer e.publish(TopicStatus)
	previous := e.authValid.Swap(valid)
	if valid {
		e.metrics.AuthState.Set(1)
		if !previous {
			e.logger.Info("Bilibili authentication state changed", "authenticated", true)
		}
		wasEverValid := e.authEverValid.Swap(true)
		if !previous && wasEverValid {
			e.enqueueSystem("B站登录已恢复，动态采集重新开始。")
		}
	} else {
		e.metrics.AuthState.Set(0)
		if previous {
			e.logger.Warn("Bilibili authentication state changed", "authenticated", false)
		}
		if previous && e.authEverValid.Load() {
			e.enqueueSystem("B站登录失效，请在管理控制台重新扫码登录。")
		}
	}
}

func (e *Engine) enqueueSystem(summary string) {
	channels, err := e.store.ListChannels()
	if err != nil {
		e.logger.Error("unable to list channels for system alert", "err", err)
		return
	}
	ids := enabledChannelIDs(channels)
	if len(ids) == 0 {
		return
	}
	now := time.Now().UTC()
	dynamic := model.Dynamic{
		ID: fmt.Sprintf("system:%d", now.UnixNano()), UID: "system", UPName: "Bili Notify", Type: "SYSTEM",
		PublishedAt: now, Summary: summary, URL: "",
	}
	if _, err := e.store.RecordDynamics("system", []model.Dynamic{dynamic}, ids, false); err != nil {
		e.logger.Error("unable to queue system alert", "err", err)
		return
	}
	e.publish(TopicStatus | TopicDeliveries)
}

func (e *Engine) StartLogin(ctx context.Context) (LoginSession, error) {
	login, err := e.client.GenerateQR(ctx)
	if err != nil {
		return LoginSession{}, err
	}
	session := LoginSession{Key: login.Key, URL: login.URL, Status: bilibili.QRWaiting, ExpiresAt: time.Now().Add(3 * time.Minute).UTC()}
	e.loginMu.Lock()
	if e.runCtx == nil {
		e.loginMu.Unlock()
		return LoginSession{}, errors.New("notification engine is not running")
	}
	if e.loginCancel != nil {
		e.loginCancel()
	}
	loginCtx, cancel := context.WithCancel(e.runCtx)
	e.login = &session
	e.loginCancel = cancel
	e.loginWG.Add(1)
	e.loginMu.Unlock()
	e.logger.Info("Bilibili QR login started", "expires_at", session.ExpiresAt)
	e.publish(TopicBiliLogin)
	go func() {
		defer e.loginWG.Done()
		e.pollLoginLoop(loginCtx, session.Key)
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
			pollCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
			login, err := e.PollLogin(pollCtx, id)
			cancel()
			if err != nil {
				e.logger.Warn("Bilibili QR login poll failed", "err", err)
				e.publish(TopicBiliLogin)
				continue
			}
			e.publish(TopicBiliLogin | TopicStatus)
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
		e.logger.Info("Bilibili QR login status changed", "status", status)
	}
	if status == bilibili.QRSuccess {
		e.client.SetSession(session)
		if _, err := e.client.ValidateSession(ctx); err != nil {
			e.client.ClearSession()
			return LoginSession{}, fmt.Errorf("validating new session: %w", err)
		}
		if err := e.store.SaveSession(session); err != nil {
			return LoginSession{}, err
		}
		e.setAuth(true)
		e.logger.Info("Bilibili QR login completed")
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
	if e.login != nil && e.login.Key == id {
		if e.loginCancel != nil {
			e.loginCancel()
		}
		e.loginCancel = nil
		e.login = nil
	}
	e.loginMu.Unlock()
	e.publish(TopicBiliLogin)
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
		e.logger.Warn("notification channel test failed", "channel_id", channel.ID, "channel_type", channel.Type, "duration", elapsed(started), "err", err)
		return err
	}
	e.logger.Info("notification channel test succeeded", "channel_id", channel.ID, "channel_type", channel.Type, "duration", elapsed(started))
	return nil
}

func (e *Engine) newSender(channel model.Channel) (notify.Sender, error) {
	return notify.NewSender(channel, nil, func(settings map[string]string) error {
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
	auth, err := notify.StartMicrosoftDeviceAuth(ctx, channel.Settings, nil)
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
	e.logger.Info("Microsoft authorization started", "channel_id", channelID, "tenant", channel.Settings["tenant"], "expires_at", session.ExpiresAt)
	e.publish(TopicMicrosoftLogin)
	go func() {
		defer e.microsoftLoginWG.Done()
		e.completeMicrosoftLogin(loginCtx, session)
	}()
	return public, nil
}

func (e *Engine) completeMicrosoftLogin(ctx context.Context, session *MicrosoftLoginSession) {
	defer e.publish(TopicMicrosoftLogin | TopicChannels | TopicStatus | TopicDeliveries)
	settings, err := session.auth.Exchange(ctx, nil)
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
		e.logger.Info("Microsoft authorization completed", "channel_id", session.ChannelID)
		return
	}
	if errors.Is(err, context.Canceled) {
		session.Status = "canceled"
		e.logger.Info("Microsoft authorization canceled", "channel_id", session.ChannelID)
		return
	}
	if !session.ExpiresAt.IsZero() && time.Now().After(session.ExpiresAt) {
		session.Status = "expired"
	} else {
		session.Status = "failed"
	}
	session.Error = err.Error()
	e.logger.Warn("Microsoft authorization failed", "channel_id", session.ChannelID, "status", session.Status, "err", err)
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
	defer e.microsoftLoginMu.Unlock()
	if session := e.microsoftLogins[channelID]; session != nil {
		if session.cancel != nil {
			session.cancel()
		}
		session.Status = "canceled"
	}
	e.publish(TopicMicrosoftLogin)
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
	status := Status{AuthValid: e.authValid.Load(), UPCount: len(ups), ChannelCount: len(channels), OutboxDepth: len(deliveries)}
	if unix := e.lastSuccess.Load(); unix > 0 {
		status.LastSuccessAt = time.Unix(unix, 0).UTC()
	}
	if len(deliveries) > 0 {
		status.OldestDelivery = oldestDelivery(deliveries)
	}
	status.Ready = status.AuthValid && enabledUPCount(ups) > 0 && len(enabledChannelIDs(channels)) > 0
	if until := e.riskUntil.Load(); until > time.Now().Unix() {
		status.RiskPausedUntil = time.Unix(until, 0).UTC()
		status.Ready = false
	}
	if status.Ready && (status.LastSuccessAt.IsZero() || time.Since(status.LastSuccessAt) > 2*time.Minute) {
		status.Ready = false
	}
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
