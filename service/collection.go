package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
)

func discoveryItem(up model.UP, raw json.RawMessage) state.CollectionItem {
	var identity struct {
		ID string `json:"id_str"`
	}
	_ = json.Unmarshal(raw, &identity)
	if identity.ID == "" {
		identity.ID = fmt.Sprintf("raw:%x", sha256.Sum256(raw))
	}
	return state.CollectionItem{SourceID: model.SourceID(model.PlatformBilibili, up.UID), ID: identity.ID, Kind: "dynamic", Payload: raw, BaselineMode: collectionBaselineMode(up)}
}

func collectionBaselineMode(up model.UP) state.DynamicBaselineMode {
	if !up.BaselineReady {
		return state.DynamicBaselineAll
	}
	if !up.ExclusiveBaselineReady {
		return state.DynamicBaselineExclusive
	}
	return state.DynamicBaselineNone
}

// discoverSpace checkpoints every fetched page. The configured page count is a
// per-cycle work budget, not a permanent ceiling on recoverable history.
func (e *Engine) discoverSpace(ctx context.Context, up model.UP) (bool, error) {
	store := e.store.WithContext(ctx)
	workCtx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	scan, err := store.CollectionScan(up.UID)
	if err != nil {
		return false, err
	}
	if scan.NextAt > time.Now().Unix() {
		return false, nil
	}
	if !scan.Active || !up.ExclusiveBaselineReady {
		scan.BaselineMode = collectionBaselineMode(up)
	}
	scan.Active = true
	for range e.Settings().BilibiliMaxDynamicPages {
		if workCtx.Err() != nil {
			return false, nil
		}
		requestCtx, cancel := context.WithTimeout(workCtx, e.httpTimeout)
		page, fetchErr := e.client.FetchRawPage(requestCtx, up.UID, scan.Offset)
		cancel()
		if fetchErr != nil {
			return false, e.deferSpaceScan(ctx, scan, fetchErr)
		}
		items := make([]state.CollectionItem, 0, len(page.Items))
		foundKnown := false
		for _, raw := range page.Items {
			item := discoveryItem(up, raw)
			item.BaselineMode = scan.BaselineMode
			known, err := store.CollectionKnown(up.UID, item.ID, scan)
			if err != nil {
				return false, err
			}
			seen, err := store.Seen(up.UID, item.ID)
			if err != nil {
				return false, err
			}
			if !seen {
				items = append(items, item)
			}
			if known {
				foundKnown = true
				break
			}
		}
		complete := !up.BaselineReady || foundKnown || !page.HasMore
		previousOffset := scan.Offset
		scan.Offset, scan.Attempts, scan.NextAt, scan.LastError = page.Offset, 0, 0, ""
		if complete {
			scan.Offset = ""
			scan.Active = false
		}
		// Even an invalid next cursor must not discard this successfully fetched page.
		if !complete && (page.Offset == "" || page.Offset == previousOffset) {
			scan.Offset = ""
			if err := store.StageCollectionPage(items, &scan); err != nil {
				return false, err
			}
			return false, e.deferSpaceScan(ctx, scan, &bilibili.APIError{Kind: bilibili.ErrorSchema, Message: "space pagination offset did not advance; restarting scan"})
		}
		if err := store.StageCollectionPage(items, &scan); err != nil {
			return false, err
		}
		if complete {
			return true, nil
		}
	}
	return false, nil
}

func (e *Engine) deferSpaceScan(ctx context.Context, scan state.CollectionScan, cause error) error {
	e.handleBiliAPIError(ctx, cause)
	scan.Attempts = min(scan.Attempts, 1000) + 1
	scan.NextAt = time.Now().Add(state.CollectionRetryDelay(scan.Attempts)).Unix()
	scan.LastError = cause.Error()
	if err := e.store.WithContext(ctx).StageCollectionPage(nil, &scan); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// processDiscoveries gives each item its own commit and bounded attempt. Failed
// items remain durable and cannot be skipped by a newer successful seen marker.
func (e *Engine) processDiscoveries(ctx context.Context, up model.UP) ([]model.Dynamic, int, error) {
	store := e.store.WithContext(ctx)
	items, err := store.DueCollectionItems(up.UID, "dynamic", time.Now(), 100)
	if err != nil {
		return nil, 0, err
	}
	type parsedItem struct {
		item    state.CollectionItem
		dynamic model.Dynamic
		err     error
	}
	parsed := make([]parsedItem, len(items))
	for i, item := range items {
		dynamic, itemErr := bilibili.ParseSpaceDynamic(up.UID, item.Payload)
		parsed[i] = parsedItem{item: item, dynamic: dynamic, err: itemErr}
	}
	slices.SortStableFunc(parsed, func(a, b parsedItem) int {
		return cmp.Or(a.dynamic.PublishedAt.Compare(b.dynamic.PublishedAt), cmp.Compare(a.item.ID, b.item.ID))
	})
	workCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var collected []model.Dynamic
	var failures []error
	created := 0
	for _, current := range parsed {
		if workCtx.Err() != nil {
			break
		}
		item, dynamic, itemErr := current.item, current.dynamic, current.err
		// A transient malformed card may have changed upstream. Refresh the head
		// on a scheduled parse retry, even when normal discovery stops at a newer
		// seen item. Retain the original payload if the card is no longer present.
		if itemErr != nil && !bilibili.IsDynamicBlocked(itemErr) && item.Attempts > 0 {
			requestCtx, cancel := context.WithTimeout(workCtx, e.httpTimeout)
			page, refreshErr := e.client.FetchRawPage(requestCtx, up.UID, "")
			cancel()
			if refreshErr != nil {
				itemErr = refreshErr
			} else {
				for _, raw := range page.Items {
					if discoveryItem(up, raw).ID == item.ID {
						item.Payload = raw
						if err := store.StageCollectionPage([]state.CollectionItem{item}, nil); err != nil {
							return collected, created, err
						}
						dynamic, itemErr = bilibili.ParseSpaceDynamic(up.UID, raw)
						break
					}
				}
			}
		}
		if bilibili.IsDynamicBlocked(itemErr) || (itemErr == nil && dynamic.Type == "DYNAMIC_TYPE_LIVE_RCMD") {
			if err := store.CompleteCollectionItem(item); err != nil {
				return collected, created, err
			}
			continue
		}
		if itemErr == nil && dynamic.UID != up.UID {
			itemErr = errors.New("space dynamic author does not match source")
		}
		if itemErr == nil && bilibili.NeedsArticleEnrichment(dynamic) {
			requestCtx, cancel := context.WithTimeout(workCtx, e.httpTimeout)
			itemErr = e.client.EnrichArticle(requestCtx, &dynamic)
			cancel()
		}
		if itemErr == nil {
			batch := []model.Dynamic{dynamic}
			e.enrichMedia(workCtx, batch)
			dynamic = batch[0]
			var count int
			count, itemErr = store.RecordCollectedDynamic(item, dynamic)
			if itemErr == nil {
				created += count
				collected = append(collected, dynamic)
			}
		}
		if itemErr != nil {
			if ctx.Err() != nil {
				return collected, created, ctx.Err()
			}
			if err := store.FailCollectionItem(item, time.Now(), itemErr); err != nil {
				return collected, created, err
			}
			e.handleBiliAPIError(ctx, itemErr)
			e.logger.WarnContext(ctx, "dynamic deferred for retry", "event", "bilibili.dynamic.deferred", "up_uid", up.UID, "dynamic_id", item.ID, "attempt", item.Attempts+1, "error", itemErr)
			failures = append(failures, itemErr)
			if bilibili.IsAuthentication(itemErr) || bilibili.IsRiskControl(itemErr) {
				break
			}
		}
	}
	if len(collected) > 0 {
		e.publish(TopicStatus | TopicDeliveries | TopicDynamics | TopicAIJobs)
	}
	return collected, created, errors.Join(failures...)
}
