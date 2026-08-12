package zsxq

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/linxin2429/bili_notify/media"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
	"golang.org/x/sync/errgroup"
)

// Collector owns the ZSXQ dynamic and comment schedules. HTTP request budgets
// and risk pauses are enforced by the shared ZSXQ transport gate.
type Collector struct {
	store     collectorStore
	client    *Client
	settings  func() model.RuntimeSettings
	logger    *slog.Logger
	assets    *media.AttachmentDownloader
	riskUntil atomic.Int64
	pause     func(time.Time)
	metrics   workflowMetrics
}

type collectorStore interface {
	ArchiveContent(model.Content, []model.Attachment) error
	ArchiveContentAndEnqueue(model.Content, []model.Attachment, []string, bool) error
	CommentSyncState(model.Platform, string) (bool, error)
	ListSources(model.Platform) ([]model.Source, error)
	MarkContentDeleted(string, time.Time) error
	PlatformAccount(model.Platform) (model.PlatformAccount, error)
	PutCommentSyncState(model.Platform, string, bool, time.Time, string) error
	PutPlatformAccount(model.PlatformAccount) error
	PutSource(model.Source) error
	QueryContents(state.PlatformContentQuery) ([]model.Content, error)
	RecordDynamics(string, []model.Dynamic, []string, state.DynamicBaselineMode) (int, error)
	SyncCommentTree(model.Content, []model.CommentNode, bool, bool, string, *model.CommentTarget) ([]model.CommentDigest, error)
}

type workflowMetrics interface {
	RecordPlatformWorkflow(context.Context, string, string, string, time.Duration)
}

func (collector *Collector) SetAttachmentDownloader(downloader *media.AttachmentDownloader) {
	collector.assets = downloader
}

func (collector *Collector) SetMetrics(metrics workflowMetrics) {
	collector.metrics = metrics
}

func (collector *Collector) SetRequestPause(pause func(time.Time)) {
	if pause != nil {
		collector.pause = pause
	}
}

func NewCollector(store collectorStore, client *Client, settings func() model.RuntimeSettings, logger *slog.Logger) (*Collector, error) {
	if store == nil || client == nil || settings == nil {
		return nil, errors.New("zsxq collector store, client and settings are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Collector{store: store, client: client, settings: settings, logger: logger, pause: func(time.Time) {}}, nil
}

func (collector *Collector) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return collector.runDynamicWorker(groupCtx) })
	group.Go(func() error { return collector.runCommentWorker(groupCtx) })
	return group.Wait()
}

func (collector *Collector) runDynamicWorker(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			started := time.Now()
			err := collector.SyncDynamics(ctx)
			collector.recordWorkflow(ctx, "dynamic_sync", err, started)
			if err != nil && !errors.Is(err, context.Canceled) {
				collector.logger.WarnContext(ctx, "Knowledge Planet dynamic synchronization failed", "event", "zsxq.dynamic.failed", "error", publicError(err))
			}
			timer.Reset(time.Duration(collector.settings().ZSXQDynamicIntervalSec) * time.Second)
		}
	}
}

func (collector *Collector) runCommentWorker(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			settings := collector.settings()
			if settings.ZSXQCommentsEnabled {
				started := time.Now()
				err := collector.SyncComments(ctx)
				collector.recordWorkflow(ctx, "comment_tree", err, started)
				if err != nil && !errors.Is(err, context.Canceled) {
					collector.logger.WarnContext(ctx, "Knowledge Planet comment synchronization failed", "event", "zsxq.comment.failed", "error", publicError(err))
				}
			}
			timer.Reset(time.Duration(settings.ZSXQCommentIntervalSec) * time.Second)
		}
	}
}

func (collector *Collector) recordWorkflow(ctx context.Context, workflow string, err error, started time.Time) {
	if collector.metrics == nil {
		return
	}
	result := "success"
	if err != nil {
		result = "error"
	}
	collector.metrics.RecordPlatformWorkflow(ctx, string(model.PlatformZSXQ), workflow, result, time.Since(started))
}

func (collector *Collector) SyncDynamics(ctx context.Context) error {
	now := time.Now()
	if pausedUntil := collector.riskUntil.Load(); pausedUntil > now.Unix() {
		return fmt.Errorf("risk pause active until %s", time.Unix(pausedUntil, 0).Format(time.RFC3339))
	}
	account, err := collector.store.PlatformAccount(model.PlatformZSXQ)
	if err != nil {
		return err
	}
	if account.RiskPausedUntil.After(now) {
		collector.riskUntil.Store(account.RiskPausedUntil.Unix())
		return fmt.Errorf("risk pause active until %s", account.RiskPausedUntil.Format(time.RFC3339))
	}
	if account.Status == model.AccountRiskPaused {
		account.Status, account.RiskPausedUntil, account.LastError = model.AccountConnected, time.Time{}, ""
		if err := collector.store.PutPlatformAccount(account); err != nil {
			return err
		}
	}
	if account.Status != model.AccountConnected {
		return ErrAuthentication
	}
	collector.client.SetSession(account.Session)
	sources, err := collector.store.ListSources(model.PlatformZSXQ)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		if err := collector.syncSource(ctx, source, account.Session); err != nil {
			collector.recordSourceError(source, err)
			if errors.Is(err, ErrAuthentication) {
				collector.queueSystemAlert(ctx, fmt.Sprintf("zsxq-auth-%d", account.VerifiedAt.Unix()), "知识星球登录已失效，知识星球采集已暂停；B 站采集和通知投递不受影响。")
				account.Status, account.LastError, account.Session = model.AccountInvalid, "authentication expired", nil
				_ = collector.store.PutPlatformAccount(account)
				return err
			}
			if errors.Is(err, ErrRiskControl) || errors.Is(err, ErrRateLimited) {
				pause := time.Duration(collector.settings().ZSXQRiskPauseSec) * time.Second
				until := time.Now().Add(pause)
				collector.pause(until)
				collector.riskUntil.Store(until.Unix())
				account.Status, account.RiskPausedUntil, account.LastError = model.AccountRiskPaused, until, publicError(err)
				_ = collector.store.PutPlatformAccount(account)
				return err
			}
		}
	}
	return nil
}

func (collector *Collector) syncSource(ctx context.Context, source model.Source, cookies map[string]string) error {
	page, err := collector.client.Topics(ctx, source, "", 20)
	if err != nil {
		return err
	}
	slices.SortFunc(page.Contents, func(a, b model.Content) int {
		if order := a.PublishedAt.Compare(b.PublishedAt); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	initializing := source.BaselineState == "" || source.BaselineState == model.BaselinePending
	watermark := source.HighWatermark
	if initializing {
		watermark = newestWatermark(page.Contents)
		source.HighWatermark = watermark
		source.BackfillCursor = page.NextCursor
		source.BaselineState = model.BaselineRunning
		if page.NextCursor == "" {
			source.BaselineState = model.BaselineComplete
		}
	}
	for _, content := range page.Contents {
		content.Baseline = initializing || !isAfterWatermark(content, watermark)
		attachments := page.Attachments[content.ID]
		collector.localize(ctx, source, content, attachments, cookies)
		if err := collector.store.ArchiveContentAndEnqueue(content, attachments, nil, !content.Baseline); err != nil {
			return err
		}
	}
	if source.BaselineState == model.BaselineRunning && source.BackfillCursor != "" {
		history, err := collector.client.Topics(ctx, source, source.BackfillCursor, 20)
		if err != nil {
			return err
		}
		for _, content := range history.Contents {
			content.Baseline = true
			attachments := history.Attachments[content.ID]
			collector.localize(ctx, source, content, attachments, cookies)
			if err := collector.store.ArchiveContentAndEnqueue(content, attachments, nil, false); err != nil {
				return err
			}
			source.BackfillDone++
		}
		if len(history.Contents) == 0 || history.NextCursor == "" || history.NextCursor == source.BackfillCursor {
			source.BackfillCursor = ""
			source.BaselineState = model.BaselineComplete
		} else {
			source.BackfillCursor = history.NextCursor
		}
	}
	if newest := newestWatermark(page.Contents); compareWatermarks(newest, source.HighWatermark) > 0 {
		source.HighWatermark = newest
	}
	now := time.Now()
	source.LastPollAt, source.LastSuccessAt, source.LastError, source.ConsecutiveFails = now, now, "", 0
	return collector.store.PutSource(source)
}

func (collector *Collector) localize(ctx context.Context, source model.Source, content model.Content, attachments []model.Attachment, cookies map[string]string) {
	if collector.assets == nil || len(attachments) == 0 {
		return
	}
	settings := collector.settings()
	result := collector.assets.EnsureAttachments(ctx, model.PlatformZSXQ, source.ID, content.ID, attachments,
		int64(settings.ZSXQAssetMaxFileMiB)<<20, int64(settings.ZSXQAssetTotalBudgetGiB)<<30, cookies)
	if result.BudgetFull {
		collector.logger.WarnContext(ctx, "Knowledge Planet attachment budget exhausted", "event", "zsxq.asset.budget_exhausted")
		collector.queueSystemAlert(ctx, "zsxq-asset-budget-exhausted", "知识星球附件总预算已耗尽；新附件将只归档元数据，现有档案不会自动删除。")
	}
}

func (collector *Collector) SyncComments(ctx context.Context) error {
	if pausedUntil := collector.riskUntil.Load(); pausedUntil > time.Now().Unix() {
		return fmt.Errorf("risk pause active until %s", time.Unix(pausedUntil, 0).Format(time.RFC3339))
	}
	account, err := collector.store.PlatformAccount(model.PlatformZSXQ)
	if err != nil {
		return err
	}
	if account.RiskPausedUntil.After(time.Now()) {
		collector.riskUntil.Store(account.RiskPausedUntil.Unix())
		return fmt.Errorf("risk pause active until %s", account.RiskPausedUntil.Format(time.RFC3339))
	}
	if account.Status != model.AccountConnected {
		return ErrAuthentication
	}
	collector.client.SetSession(account.Session)
	sources, err := collector.store.ListSources(model.PlatformZSXQ)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		var afterAt time.Time
		var afterID string
		for {
			contents, err := collector.store.QueryContents(state.PlatformContentQuery{Platform: model.PlatformZSXQ, SourceID: source.ID, Limit: 100, AfterAt: afterAt, AfterID: afterID})
			if err != nil {
				return err
			}
			for _, archived := range contents {
				content, attachments, err := collector.client.Topic(ctx, source, archived.ExternalID)
				if err != nil {
					if collector.stopPlatformOnError(account, err) {
						if errors.Is(err, ErrAuthentication) {
							collector.queueSystemAlert(ctx, fmt.Sprintf("zsxq-auth-%d", account.VerifiedAt.Unix()), "知识星球登录已失效，知识星球采集已暂停；B 站采集和通知投递不受影响。")
						}
						return err
					}
					if errors.Is(err, ErrRemoteNotFound) {
						_ = collector.store.MarkContentDeleted(archived.ID, time.Now())
					}
					_ = collector.store.PutCommentSyncState(model.PlatformZSXQ, archived.ID, false, time.Now(), publicError(err))
					continue
				}
				content.Baseline = archived.Baseline
				collector.localize(ctx, source, content, attachments, account.Session)
				if err := collector.store.ArchiveContent(content, attachments); err != nil {
					return err
				}
				nodes, complete, err := collector.allComments(ctx, content, source.OwnerID)
				if err != nil {
					if collector.stopPlatformOnError(account, err) {
						if errors.Is(err, ErrAuthentication) {
							collector.queueSystemAlert(ctx, fmt.Sprintf("zsxq-auth-%d", account.VerifiedAt.Unix()), "知识星球登录已失效，知识星球采集已暂停；B 站采集和通知投递不受影响。")
						}
						return err
					}
					_ = collector.store.PutCommentSyncState(model.PlatformZSXQ, content.ID, false, time.Now(), publicError(err))
					continue
				}
				baselineReady, err := collector.store.CommentSyncState(model.PlatformZSXQ, content.ID)
				if err != nil {
					return err
				}
				batchID := fmt.Sprintf("zsxq-%d", time.Now().UnixNano())
				if _, err := collector.store.SyncCommentTree(content, nodes, complete, !baselineReady, batchID, nil); err != nil {
					return err
				}
			}
			if len(contents) < 100 {
				break
			}
			last := contents[len(contents)-1]
			afterAt, afterID = last.PublishedAt, last.ID
		}
		source.LastCommentAt = time.Now()
		_ = collector.store.PutSource(source)
	}
	return nil
}

func (collector *Collector) stopPlatformOnError(account model.PlatformAccount, err error) bool {
	if errors.Is(err, ErrAuthentication) {
		account.Status, account.LastError, account.Session = model.AccountInvalid, "authentication expired", nil
		_ = collector.store.PutPlatformAccount(account)
		return true
	}
	if errors.Is(err, ErrRiskControl) || errors.Is(err, ErrRateLimited) {
		until := time.Now().Add(time.Duration(collector.settings().ZSXQRiskPauseSec) * time.Second)
		collector.pause(until)
		collector.riskUntil.Store(until.Unix())
		account.Status, account.RiskPausedUntil, account.LastError = model.AccountRiskPaused, until, publicError(err)
		_ = collector.store.PutPlatformAccount(account)
		return true
	}
	return false
}

func (collector *Collector) allComments(ctx context.Context, content model.Content, ownerID string) ([]model.CommentNode, bool, error) {
	var nodes []model.CommentNode
	cursor := ""
	seenCursors := make(map[string]bool)
	for pageNumber := 0; pageNumber < 10000; pageNumber++ {
		page, err := collector.client.Comments(ctx, content, ownerID, cursor, 100)
		if err != nil {
			return nodes, false, err
		}
		nodes = append(nodes, page.Nodes...)
		if len(page.Nodes) == 0 || page.NextCursor == "" {
			return nodes, true, nil
		}
		if page.NextCursor == cursor || seenCursors[page.NextCursor] {
			return nodes, false, ErrSchemaDrift
		}
		seenCursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return nodes, false, errors.New("zsxq comment pagination exceeded safety limit")
}

func (collector *Collector) recordSourceError(source model.Source, err error) {
	source.LastPollAt = time.Now()
	source.LastError = publicError(err)
	source.ConsecutiveFails++
	_ = collector.store.PutSource(source)
}

func (collector *Collector) queueSystemAlert(ctx context.Context, id, message string) {
	dynamic := model.Dynamic{ID: "system:" + id, UID: "system", UPName: "Bili Notify", Type: "SYSTEM",
		PublishedAt: time.Now(), Summary: message}
	if _, err := collector.store.RecordDynamics("system", []model.Dynamic{dynamic}, nil, state.DynamicBaselineNone); err != nil {
		collector.logger.ErrorContext(ctx, "unable to queue Knowledge Planet system alert", "event", "zsxq.alert.queue_failed", "error", publicError(err))
	}
}

func newestWatermark(contents []model.Content) string {
	if len(contents) == 0 {
		return ""
	}
	newest := contents[0]
	for _, content := range contents[1:] {
		if content.PublishedAt.After(newest.PublishedAt) || (content.PublishedAt.Equal(newest.PublishedAt) && content.ID > newest.ID) {
			newest = content
		}
	}
	return encodeWatermark(newest)
}

func isAfterWatermark(content model.Content, watermark string) bool {
	return watermark != "" && compareWatermarks(encodeWatermark(content), watermark) > 0
}

func encodeWatermark(content model.Content) string {
	return content.PublishedAt.UTC().Format(time.RFC3339Nano) + "|" + content.ID
}

func compareWatermarks(left, right string) int {
	leftTime, leftID, leftOK := decodeWatermark(left)
	rightTime, rightID, rightOK := decodeWatermark(right)
	if leftOK && rightOK {
		if order := leftTime.Compare(rightTime); order != 0 {
			return order
		}
		return cmp.Compare(leftID, rightID)
	}
	return cmp.Compare(left, right)
}

func decodeWatermark(value string) (time.Time, string, bool) {
	stamp, id, found := strings.Cut(value, "|")
	if !found || id == "" {
		return time.Time{}, "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, stamp)
	return parsed, id, err == nil
}

func publicError(err error) string {
	switch {
	case errors.Is(err, ErrAuthentication):
		return "authentication expired"
	case errors.Is(err, ErrRateLimited):
		return "request rate limited"
	case errors.Is(err, ErrRiskControl):
		return "risk control pause"
	case errors.Is(err, ErrSchemaDrift):
		return "upstream response schema changed"
	case errors.Is(err, ErrRemoteNotFound):
		return "upstream content deleted"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err.Error()
	default:
		return "upstream synchronization failed"
	}
}
