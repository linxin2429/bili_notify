package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/service/bilisession"
	"github.com/linxin2429/bili_notify/state"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type sessionStore struct{ store *state.Store }

func (s sessionStore) Save(ctx context.Context, session model.BiliSession) error {
	return s.store.WithContext(ctx).SaveSession(session)
}

func (e *Engine) authLoop(ctx context.Context) error {
	return bilisession.Run(ctx, time.Tick(sessionValidationInterval), e.sessionWake, e.maintainBiliSession)
}

func (e *Engine) wakeSessionMaintenance() {
	select {
	case e.sessionWake <- struct{}{}:
	default:
	}
}

func (e *Engine) maintainBiliSession(parent context.Context) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	// Start the timeout after acquiring the lock, so collection cannot consume
	// the network budget. Cancellation is checked before any side effects.
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	ctx, span := e.tracer.Start(ctx, "bilibili.session.maintain")
	defer span.End()
	started := time.Now()
	result, err := e.maintainBiliSessionOnce(ctx)
	if errors.Is(err, context.Canceled) {
		result = "canceled"
	}
	if err != nil && result != "canceled" {
		span.SetStatus(codes.Error, "Bilibili session maintenance failed")
		if phase, ok := errors.AsType[*bilisession.OperationError](err); ok {
			span.SetAttributes(attribute.String("phase", phase.Phase))
		}
		e.handleSessionMaintenanceError(ctx, err)
	}
	span.SetAttributes(attribute.String("platform", "bilibili"), attribute.String("workflow", "session_maintenance"), attribute.String("result", result))
	e.metrics.RecordPlatformWorkflow(ctx, "bilibili", "session_maintenance", result, time.Since(started))
	attrs := []any{"event", "bilibili.session.maintenance_completed", "result", result, "duration_ms", elapsedMS(started)}
	if result == "renewed" || result == "confirmed" {
		e.logger.InfoContext(ctx, "Bilibili session maintenance completed", attrs...)
	} else {
		e.logger.DebugContext(ctx, "Bilibili session maintenance completed", attrs...)
	}
}

// The outcome describes the entire durable workflow: activating new cookies
// alone is not reported as completed renewal until confirmation is saved.
func (e *Engine) maintainBiliSessionOnce(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "canceled", err
	}
	now := time.Now()
	if now.Before(e.sessionRetryAt) || now.Unix() < e.riskUntil.Load() {
		return "skipped", nil
	}
	session, err := e.store.WithContext(ctx).Session()
	if errors.Is(err, state.ErrNotFound) {
		return "skipped", nil
	}
	if err != nil {
		return "error", &bilisession.OperationError{Phase: "load", Err: err}
	}
	account, err := e.sessionClient.Validate(ctx, session)
	if err != nil {
		if bilibili.IsAuthentication(err) {
			e.setAuth(false)
		}
		return "error", &bilisession.OperationError{Phase: "validate_current", Err: err}
	}
	if session.AccountUID != "" && account.UID != session.AccountUID {
		return "requires_login", bilisession.ErrIdentityChanged
	}
	if !e.authValid.Load() || session.AccountUID != account.UID || session.AccountName != account.Name {
		session.AccountUID, session.AccountName = account.UID, account.Name
		if err := e.store.WithContext(ctx).SaveSession(session); err != nil {
			return "error", &bilisession.OperationError{Phase: "save_identity", Err: err}
		}
	}
	e.setAccount(account)
	e.setAuth(true)
	result := "not_due"
	if session.PendingRefreshToken != "" {
		result = "confirmed"
	}
	manager := bilisession.Manager{
		Protocol: e.sessionClient, Store: sessionStore{e.store},
		Activate: func(next model.BiliSession) {
			result = "renewed"
			e.client.SetSession(next)
			e.setAccount(model.BiliAccount{UID: next.AccountUID, Name: next.AccountName})
			e.publish(TopicStatus)
			e.logger.InfoContext(ctx, "Bilibili session renewed; confirmation pending", "event", "bilibili.session.renewed", "result", "pending_confirmation")
		},
	}
	if err := manager.Maintain(ctx, session); err != nil {
		if errors.Is(err, bilisession.ErrMissingCredentials) || errors.Is(err, bilibili.ErrRefreshRejected) || errors.Is(err, bilisession.ErrIdentityChanged) {
			return "requires_login", err
		}
		return "error", err
	}
	return result, nil
}

// Renewal rejection is distinct from login rejection. The next scheduled
// validation decides whether collection can continue.
func (e *Engine) handleSessionMaintenanceError(ctx context.Context, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, bilisession.ErrMissingCredentials) || errors.Is(err, bilibili.ErrRefreshRejected) || errors.Is(err, bilisession.ErrIdentityChanged) {
		if !e.sessionWarning {
			e.sessionWarning = true
			e.logger.WarnContext(ctx, "Bilibili automatic renewal requires QR login", "event", "bilibili.session.renewal_requires_login", "result", "requires_login")
			e.enqueueSystem("B站登录暂无法自动续期，请在管理控制台重新扫码登录以启用自动续期；当前有效会话仍可继续采集。")
		}
		return
	}
	if bilibili.IsRiskControl(err) {
		e.handleBiliAPIError(err)
	}
	if apiErr, ok := errors.AsType[*bilibili.APIError](err); ok && apiErr.RetryAfter > 0 {
		e.sessionRetryAt = time.Now().Add(apiErr.RetryAfter)
	}
	// Error text from storage or arbitrary transports may contain secrets.
	attrs := []any{"event", "bilibili.session.maintenance_failed", "result", "error"}
	if phase, ok := errors.AsType[*bilisession.OperationError](err); ok {
		attrs = append(attrs, "phase", phase.Phase)
	}
	if apiErr, ok := errors.AsType[*bilibili.APIError](err); ok {
		attrs = append(attrs, "kind", apiErr.Kind, "http_status", apiErr.HTTPStatus, "code", apiErr.Code)
	}
	e.logger.WarnContext(ctx, "Bilibili session maintenance failed; will retry", attrs...)
}

func (e *Engine) restoreBiliSession(ctx context.Context) (err error) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	ctx, span := e.tracer.Start(ctx, "bilibili.session.restore")
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "Bilibili session restore failed")
		}
		span.End()
	}()
	store := e.store.WithContext(ctx)
	if session, err := store.Session(); err == nil {
		e.client.SetSession(session)
		validateCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
		account, validateErr := e.sessionClient.Validate(validateCtx, session)
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
			e.logger.InfoContext(ctx, "stored Bilibili session restored", "event", "bilibili.session.restored", "result", "success")
		} else if bilibili.IsAuthentication(validateErr) {
			span.SetStatus(codes.Error, "Bilibili login is invalid")
			e.authEverValid.Store(true)
			if statusErr := store.SetPlatformAccountStatus(model.PlatformBilibili, model.AccountInvalid, "session validation failed"); statusErr != nil {
				return fmt.Errorf("marking invalid Bilibili session: %w", statusErr)
			}
			e.logger.WarnContext(ctx, "stored Bilibili session is invalid", "event", "bilibili.session.invalid", "result", "failure", "error", validateErr)
			e.enqueueSystem("B站登录失效，请在管理控制台重新扫码登录。")
		} else {
			span.SetStatus(codes.Error, "Bilibili startup validation unavailable")
			e.logger.WarnContext(ctx, "Bilibili startup validation unavailable; will retry", "event", "bilibili.session.validation_deferred")
			e.handleSessionMaintenanceError(ctx, &bilisession.OperationError{Phase: "startup_validation", Err: validateErr})
		}
	} else if errors.Is(err, context.Canceled) {
		return nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return fmt.Errorf("loading Bilibili session: %w", err)
	}

	return nil
}
