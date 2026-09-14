// Package bilisession maintains Bilibili login credentials independently of
// collection, notifications, HTTP handlers and database implementations.
package bilisession

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
)

var (
	ErrMissingCredentials = errors.New("Bilibili login requires another QR scan to enable renewal")
	ErrIdentityChanged    = errors.New("renewed Bilibili account identity changed")
)

// OperationError identifies a safe workflow phase without requiring callers
// to log the wrapped error, which may originate from secret storage.
type OperationError struct {
	Phase string
	Err   error
}

func (e *OperationError) Error() string { return fmt.Sprintf("%s: %v", e.Phase, e.Err) }
func (e *OperationError) Unwrap() error { return e.Err }

type Protocol interface {
	Check(context.Context, model.BiliSession) (bilibili.RefreshInfo, error)
	Refresh(context.Context, model.BiliSession, int64) (model.BiliSession, error)
	Validate(context.Context, model.BiliSession) (model.BiliAccount, error)
	Confirm(context.Context, model.BiliSession) error
}

type Store interface {
	Save(context.Context, model.BiliSession) error
}

// Manager runs under its owner's session write lock. Activate must be an
// infallible in-memory replacement; it is only called after durable storage.
type Manager struct {
	Protocol Protocol
	Store    Store
	Activate func(model.BiliSession)
	Now      func() time.Time
}

// Maintain rotates one snapshot or resumes its outstanding confirmation.
// A failed confirmation leaves the newly saved credentials active.
func (m Manager) Maintain(ctx context.Context, session model.BiliSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session.PendingRefreshToken != "" {
		return m.confirm(ctx, session)
	}
	if session.RefreshToken == "" || session.Cookies["bili_jct"] == "" {
		return ErrMissingCredentials
	}
	info, err := m.Protocol.Check(ctx, session)
	if err != nil {
		return &OperationError{Phase: "check", Err: err}
	}
	if !info.Required {
		return nil
	}
	next, err := m.Protocol.Refresh(ctx, session, info.Timestamp)
	if err != nil {
		return &OperationError{Phase: "refresh", Err: err}
	}
	account, err := m.Protocol.Validate(ctx, next)
	if err != nil {
		return &OperationError{Phase: "validate_new", Err: err}
	}
	if account.UID == "" || account.UID != session.AccountUID {
		return ErrIdentityChanged
	}
	next.AccountUID, next.AccountName = account.UID, account.Name
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	next.UpdatedAt = now()
	next.PendingRefreshToken = session.RefreshToken
	if err := m.Store.Save(ctx, next); err != nil {
		return &OperationError{Phase: "save_new", Err: err}
	}
	m.Activate(next)
	return m.confirm(ctx, next)
}

func (m Manager) confirm(ctx context.Context, session model.BiliSession) error {
	if err := m.Protocol.Confirm(ctx, session); err != nil {
		return &OperationError{Phase: "confirm", Err: err}
	}
	session.PendingRefreshToken = ""
	if err := m.Store.Save(ctx, session); err != nil {
		return &OperationError{Phase: "save_confirmation", Err: err}
	}
	return nil
}

// Run performs an initial check and responds to periodic ticks and login
// changes. Supplying ticks allows deterministic scheduling tests.
func Run(ctx context.Context, ticks <-chan time.Time, wake <-chan struct{}, maintain func(context.Context)) error {
	if ctx.Err() != nil {
		return nil
	}
	maintain(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			maintain(ctx)
		case <-wake:
			maintain(ctx)
		}
	}
}
