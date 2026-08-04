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
	store                *state.Store
	client               *bilibili.Client
	logger               *slog.Logger
	metrics              *Metrics
	settingsMu           sync.RWMutex
	pollInterval         time.Duration
	requestRate          float64
	concurrency          int
	commentEnabled       bool
	commentTrackN        int
	commentRootPages     int
	commentReplyPages    int
	commentBatchInterval time.Duration
	limiter              *rate.Limiter
	settingsNotify       chan struct{}
	httpTimeout          time.Duration
	authValid            atomic.Bool
	authEverValid        atomic.Bool
	lastSuccess          atomic.Int64
	riskUntil            atomic.Int64
	backlogAlerted       atomic.Bool
	loginMu              sync.Mutex
	login                *LoginSession
	loginCancel          context.CancelFunc
	loginWG              sync.WaitGroup
	runCtx               context.Context
	microsoftSendMu      sync.Mutex
	microsoftLoginMu     sync.Mutex
	microsoftLoginWG     sync.WaitGroup
	microsoftRunCtx      context.Context
	microsoftLogins      map[string]*MicrosoftLoginSession
	events               *EventBus
}

func NewEngine(store *state.Store, client *bilibili.Client, logger *slog.Logger, metrics *Metrics, settings model.RuntimeSettings, events *EventBus) *Engine {
	if events == nil {
		events = NewEventBus()
	}
	settings = settings.WithCommentDefaults()
	return &Engine{
		store: store, client: client, logger: logger, metrics: metrics,
		pollInterval:         settings.PollInterval(),
		requestRate:          settings.RequestRate,
		concurrency:          settings.RequestConcurrency,
		commentEnabled:       settings.CommentEnabled,
		commentTrackN:        settings.CommentTrackN,
		commentRootPages:     settings.CommentRootPages,
		commentReplyPages:    settings.CommentReplyPages,
		commentBatchInterval: settings.CommentBatchInterval(),
		limiter:              rate.NewLimiter(rate.Limit(settings.RequestRate), max(1, int(settings.RequestRate))),
		settingsNotify:       make(chan struct{}, 1),
		httpTimeout:          10 * time.Second,
		microsoftLogins:      make(map[string]*MicrosoftLoginSession),
		events:               events,
	}
}

func (e *Engine) Settings() model.RuntimeSettings {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return model.RuntimeSettings{
		PollIntervalSec:         int(e.pollInterval / time.Second),
		RequestRate:             e.requestRate,
		RequestConcurrency:      e.concurrency,
		CommentEnabled:          e.commentEnabled,
		CommentTrackN:           e.commentTrackN,
		CommentRootPages:        e.commentRootPages,
		CommentReplyPages:       e.commentReplyPages,
		CommentBatchIntervalSec: int(e.commentBatchInterval / time.Second),
	}
}

func (e *Engine) UpdateSettings(settings model.RuntimeSettings) error {
	settings = settings.WithCommentDefaults()
	if err := settings.Validate(); err != nil {
		return err
	}
	if e.Settings() == settings {
		return nil
	}
	if err := e.store.PutRuntimeSettings(settings); err != nil {
		return err
	}
	e.applySettings(settings)
	e.publish(TopicSettings | TopicStatus)
	return nil
}

func (e *Engine) applySettings(settings model.RuntimeSettings) {
	settings = settings.WithCommentDefaults()
	e.settingsMu.Lock()
	e.pollInterval = settings.PollInterval()
	e.requestRate = settings.RequestRate
	e.concurrency = settings.RequestConcurrency
	e.commentEnabled = settings.CommentEnabled
	e.commentTrackN = settings.CommentTrackN
	e.commentRootPages = settings.CommentRootPages
	e.commentReplyPages = settings.CommentReplyPages
	e.commentBatchInterval = settings.CommentBatchInterval()
	e.limiter.SetLimit(rate.Limit(settings.RequestRate))
	e.limiter.SetBurst(max(1, int(settings.RequestRate)))
	e.settingsMu.Unlock()
	select {
	case e.settingsNotify <- struct{}{}:
	default:
	}
}

func (e *Engine) currentPollInterval() time.Duration {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.pollInterval
}

func (e *Engine) currentCommentBatchInterval() time.Duration {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.commentBatchInterval
}

func (e *Engine) currentCommentSettings() (enabled bool, trackN, rootPages, replyPages int) {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.commentEnabled, e.commentTrackN, e.commentRootPages, e.commentReplyPages
}

func (e *Engine) currentConcurrency() int {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.concurrency
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
	g.Go(func() error { return e.commentLoop(ctx) })
	g.Go(func() error { return e.deliveryLoop(ctx) })
	g.Go(func() error { return e.authLoop(ctx) })
	return g.Wait()
}

func (e *Engine) collectLoop(ctx context.Context) error {
	interval := e.currentPollInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if err := e.collectOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		e.logger.Error("initial collection cycle failed", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-e.settingsNotify:
			next := e.currentPollInterval()
			if next != interval {
				ticker.Reset(next)
				interval = next
			}
		case <-ticker.C:
			if err := e.collectOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				e.logger.Error("collection cycle failed", "err", err)
			}
		}
	}
}

func (e *Engine) collectOnce(ctx context.Context) error {
	started := time.Now()
	if !e.authValid.Load() {
		e.logger.Debug("collection cycle skipped", "reason", "Bilibili session is not authenticated")
		return nil
	}
	if until := e.riskUntil.Load(); until > time.Now().Unix() {
		e.logger.Debug("collection cycle skipped", "reason", "Bilibili risk-control pause", "resume_at", time.Unix(until, 0))
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
	g.SetLimit(e.currentConcurrency())
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
					e.publish(TopicStatus)
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
	if err := e.refreshCommentTargets(up, name, items); err != nil {
		return err
	}
	now := time.Now()
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
	topics := TopicStatus | TopicUPs
	if created > 0 {
		topics |= TopicDeliveries
	}
	e.publish(topics)
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

func (e *Engine) refreshCommentTargets(up model.UP, name string, items []model.Dynamic) error {
	enabled, trackN, _, _ := e.currentCommentSettings()
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
		existing, err := e.store.ListCommentTargets(up.UID)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			return nil
		}
		_, err = e.store.UpsertCommentTargets(up.UID, nil, trackN)
		return err
	}
	_, err := e.store.UpsertCommentTargets(up.UID, discovered, trackN)
	return err
}

func (e *Engine) commentLoop(ctx context.Context) error {
	interval := e.currentCommentBatchInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if err := e.commentOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		e.logger.Error("initial comment cycle failed", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Re-read interval each tick so settings updates apply without racing
			// collectLoop for the single settingsNotify buffer.
			if next := e.currentCommentBatchInterval(); next != interval {
				ticker.Reset(next)
				interval = next
			}
			if err := e.commentOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				e.logger.Error("comment cycle failed", "err", err)
			}
		}
	}
}

func (e *Engine) commentOnce(ctx context.Context) error {
	enabled, _, rootPages, replyPages := e.currentCommentSettings()
	if !enabled {
		return nil
	}
	if !e.authValid.Load() {
		return nil
	}
	if until := e.riskUntil.Load(); until > time.Now().Unix() {
		return nil
	}
	channels, err := e.store.ListChannels()
	if err != nil {
		return fmt.Errorf("listing channels for comments: %w", err)
	}
	channelIDs := enabledChannelIDs(channels)
	if len(channelIDs) == 0 {
		return nil
	}
	ups, err := e.store.ListUPs()
	if err != nil {
		return fmt.Errorf("listing UPs for comments: %w", err)
	}
	enabledUIDs := make(map[string]model.UP)
	for _, up := range ups {
		if up.Enabled {
			enabledUIDs[up.UID] = up
		}
	}
	targets, err := e.store.ListAllCommentTargets()
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(e.currentConcurrency())
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
			return e.pollCommentTarget(gctx, target, channelIDs, rootPages, replyPages)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if scanned > 0 {
		e.logger.Debug("comment cycle completed", "targets", scanned)
	}
	return nil
}

func (e *Engine) pollCommentTarget(ctx context.Context, target model.CommentTarget, channelIDs []string, rootPages, replyPages int) error {
	requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
	defer cancel()

	upReplies := make([]bilibili.Reply, 0)
	// rootRPID -> root reply
	roots := make(map[string]bilibili.Reply)
	// child rpid -> reply
	children := make(map[string]bilibili.Reply)
	// roots that need full expansion because an UP reply lives under them
	expandRoots := make(map[string]struct{})

	for pn := 1; pn <= rootPages; pn++ {
		if pn > 1 {
			if err := e.limiter.Wait(ctx); err != nil {
				return err
			}
		}
		page, err := e.client.ListRootReplies(requestCtx, target.CommentType, target.CommentOID, pn, 20)
		if err != nil {
			return e.handleCommentPollError(target, err)
		}
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
	}

	// Scan root previews is not enough for nested-only UP replies; for each root with
	// rcount>0 we do not expand unless we already saw an UP reply under it. Nested-only
	// discovery requires expanding roots that grew — we expand any root that already
	// has an UP reply, and also roots when target is baselining (to mark all current UP replies).
	if !target.BaselineReady {
		for _, root := range roots {
			if root.RCount > 0 {
				expandRoots[root.RPID] = struct{}{}
			}
			if root.Mid == target.UID {
				// root itself is enough; no expand needed solely for that
			}
		}
	}

	// First pass already collected root-level UP replies. Expand when we need children.
	// For ongoing monitoring, expand roots only when we found a nested UP candidate is
	// impossible without scanning children. Practical approach: expand roots with rcount>0
	// only during baseline; afterwards expand roots when any root-level page shows growth
	// via comment count change. To keep nested UP replies discoverable without huge cost,
	// expand each root with rcount>0 up to replyPages but stop early if no UP mid found
	// after first child page unless baselining.
	for rootID, root := range roots {
		if root.RCount <= 0 {
			continue
		}
		if target.BaselineReady {
			// After baseline, expand only roots that may contain new activity.
			// Without per-root rcount storage, expand every root with replies within window.
			// Cap is replyPages; accept cost for N small.
		}
		expandRoots[rootID] = struct{}{}
	}

	for rootID := range expandRoots {
		for pn := 1; pn <= replyPages; pn++ {
			if err := e.limiter.Wait(ctx); err != nil {
				return err
			}
			page, err := e.client.ListChildReplies(requestCtx, target.CommentType, target.CommentOID, rootID, pn, 20)
			if err != nil {
				return e.handleCommentPollError(target, err)
			}
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
		}
	}

	// Deduplicate UP replies by rpid.
	unique := make(map[string]bilibili.Reply, len(upReplies))
	for _, reply := range upReplies {
		unique[reply.RPID] = reply
	}

	notes := make([]model.CommentNotification, 0, len(unique))
	for _, reply := range unique {
		seen, err := e.store.CommentSeen(target.UID, reply.RPID)
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
			Incomplete:   incomplete,
			Thread:       thread,
		})
	}
	slices.SortFunc(notes, func(a, b model.CommentNotification) int {
		return a.PublishedAt.Compare(b.PublishedAt)
	})

	target.LastError = ""
	created, err := e.store.RecordCommentNotifications(target, notes, channelIDs, !target.BaselineReady)
	if err != nil {
		return err
	}
	e.metrics.CommentPollTotal.WithLabelValues("success").Inc()
	if created > 0 {
		e.metrics.CommentFoundTotal.Add(float64(created))
		e.logger.Info("new UP replies queued", "uid", target.UID, "comment_oid", target.CommentOID, "reply_count", created)
		e.publish(TopicStatus | TopicDeliveries)
	}
	return nil
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

func (e *Engine) handleCommentPollError(target model.CommentTarget, pollErr error) error {
	if bilibili.IsAuthentication(pollErr) {
		e.setAuth(false)
	}
	if bilibili.IsRiskControl(pollErr) {
		now := time.Now()
		previousUntil := e.riskUntil.Swap(now.Add(5 * time.Minute).Unix())
		if previousUntil <= now.Unix() {
			e.publish(TopicStatus)
			e.enqueueSystem("B站接口触发风控，采集已暂停五分钟；服务不会尝试绕过风控。")
		}
	}
	if bilibili.IsCommentClosed(pollErr) {
		target.Closed = true
		target.LastError = pollErr.Error()
		_ = e.store.UpdateCommentTarget(target)
		e.metrics.CommentPollTotal.WithLabelValues("closed").Inc()
		e.logger.Info("comment area closed", "uid", target.UID, "comment_type", target.CommentType, "comment_oid", target.CommentOID)
		return nil
	}
	target.LastError = pollErr.Error()
	_ = e.store.UpdateCommentTarget(target)
	e.metrics.CommentPollTotal.WithLabelValues("error").Inc()
	e.logger.Warn("comment target poll failed", "uid", target.UID, "comment_oid", target.CommentOID, "err", pollErr)
	return nil
}

func (e *Engine) failPoll(up model.UP, name string, started time.Time, pollErr error) error {
	if name == "" {
		name = up.Name
	}
	e.metrics.PollTotal.WithLabelValues("error").Inc()
	if err := e.store.SetUPResult(up.UID, name, time.Now(), pollErr); err != nil {
		return fmt.Errorf("recording failed poll for UP %s: %w", up.UID, err)
	}
	e.publish(TopicStatus | TopicUPs)
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
	deliveries, err := e.store.DueDeliveries(time.Now(), 50)
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

func (e *Engine) deliver(ctx context.Context, delivery model.Delivery) (bool, error) {
	channel, err := e.store.Channel(delivery.ChannelID)
	if err != nil {
		deliveryErr := errors.New("channel no longer exists")
		if err := e.store.FailDelivery(delivery.ID, true, time.Now(), deliveryErr); err != nil {
			return false, err
		}
		e.logger.Warn("notification delivery blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", delivery.ChannelID, "attempt", delivery.Attempts+1, "err", deliveryErr)
		return true, nil
	}
	if !channel.Enabled {
		return false, nil
	}
	if channel.Type == model.ChannelMicrosoft {
		e.microsoftSendMu.Lock()
		defer e.microsoftSendMu.Unlock()
		channel, err = e.store.Channel(delivery.ChannelID)
		if err != nil {
			deliveryErr := errors.New("channel no longer exists")
			if err := e.store.FailDelivery(delivery.ID, true, time.Now(), deliveryErr); err != nil {
				return false, err
			}
			e.logger.Warn("notification delivery blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", delivery.ChannelID, "attempt", delivery.Attempts+1, "err", deliveryErr)
			return true, nil
		}
	}
	sender, err := e.newSender(channel)
	if err != nil {
		if storeErr := e.store.FailDelivery(delivery.ID, true, time.Now(), err); storeErr != nil {
			return false, storeErr
		}
		e.logger.Warn("notification delivery blocked", "delivery_id", delivery.ID, "dynamic_id", delivery.Dynamic.ID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "err", err)
		return true, nil
	}
	message, contentID, err := deliveryMessage(delivery)
	if err != nil {
		if storeErr := e.store.FailDelivery(delivery.ID, true, time.Now(), err); storeErr != nil {
			return false, storeErr
		}
		e.logger.Warn("notification delivery blocked", "delivery_id", delivery.ID, "content_id", contentID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "err", err)
		return true, nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	started := time.Now()
	err = sender.Send(sendCtx, message)
	cancel()
	e.metrics.DeliveryDuration.Observe(time.Since(started).Seconds())
	if err == nil {
		e.metrics.DeliveryTotal.WithLabelValues(string(channel.Type), "success").Inc()
		if err := e.store.CompleteDelivery(delivery.ID); err != nil {
			return false, err
		}
		e.logger.Info("notification delivered", "delivery_id", delivery.ID, "content_id", contentID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "duration", elapsed(started))
		return true, nil
	}
	blocked := notify.IsPermanent(err)
	result := "retry"
	if blocked {
		result = "blocked"
	}
	e.metrics.DeliveryTotal.WithLabelValues(string(channel.Type), result).Inc()
	next := time.Now().Add(retryDelay(delivery.Attempts))
	if storeErr := e.store.FailDelivery(delivery.ID, blocked, next, err); storeErr != nil {
		return false, storeErr
	}
	e.logger.Warn("notification delivery failed", "delivery_id", delivery.ID, "content_id", contentID, "channel_id", channel.ID, "channel_type", channel.Type, "attempt", delivery.Attempts+1, "result", result, "next_attempt_at", next, "duration", elapsed(started), "err", err)
	return true, nil
}

func deliveryMessage(delivery model.Delivery) (notify.Message, string, error) {
	switch delivery.EffectiveKind() {
	case model.DeliveryKindComment:
		if delivery.Comment == nil {
			return notify.Message{}, "", errors.New("comment delivery is missing payload")
		}
		return notify.CommentThreadMessage(*delivery.Comment), delivery.Comment.RPID, nil
	default:
		return notify.DynamicMessage(delivery.Dynamic), delivery.Dynamic.ID, nil
	}
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
	if previous != valid {
		e.publish(TopicStatus)
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
	now := time.Now()
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
			previous, _ := e.Login()
			pollCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
			login, err := e.PollLogin(pollCtx, id)
			cancel()
			if err != nil {
				e.logger.Warn("Bilibili QR login poll failed", "err", err)
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
		status.LastSuccessAt = time.Unix(unix, 0)
	}
	if len(deliveries) > 0 {
		status.OldestDelivery = oldestDelivery(deliveries)
	}
	status.Ready = status.AuthValid && enabledUPCount(ups) > 0 && len(enabledChannelIDs(channels)) > 0
	if until := e.riskUntil.Load(); until > time.Now().Unix() {
		status.RiskPausedUntil = time.Unix(until, 0)
		status.Ready = false
	}
	staleAfter := max(2*time.Minute, 2*e.currentPollInterval())
	if status.Ready && (status.LastSuccessAt.IsZero() || time.Since(status.LastSuccessAt) > staleAfter) {
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
