package bilisession

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProtocol struct {
	calls   *[]string
	failure string
	err     error
	due     bool
	uid     string
}

func (p fakeProtocol) step(name string) error {
	*p.calls = append(*p.calls, name)
	if p.failure == name {
		return p.err
	}
	return nil
}
func (p fakeProtocol) Check(context.Context, model.BiliSession) (bilibili.RefreshInfo, error) {
	return bilibili.RefreshInfo{Required: p.due, Timestamp: 123}, p.step("check")
}
func (p fakeProtocol) Refresh(_ context.Context, s model.BiliSession, _ int64) (model.BiliSession, error) {
	s.Cookies = map[string]string{"SESSDATA": "new", "bili_jct": "new-csrf"}
	s.RefreshToken = "new-token"
	return s, p.step("refresh")
}
func (p fakeProtocol) Validate(context.Context, model.BiliSession) (model.BiliAccount, error) {
	return model.BiliAccount{UID: p.uid, Name: "User"}, p.step("validate")
}
func (p fakeProtocol) Confirm(_ context.Context, s model.BiliSession) error {
	if s.PendingRefreshToken != "old-token" || s.Cookies["SESSDATA"] != "new" {
		return errors.New("wrong confirmation credentials")
	}
	return p.step("confirm")
}

type fakeStore struct {
	session model.BiliSession
	calls   *[]string
	saves   int
	failAt  int
	err     error
}

func (s *fakeStore) Save(_ context.Context, session model.BiliSession) error {
	s.saves++
	*s.calls = append(*s.calls, "save")
	if s.saves == s.failAt {
		return s.err
	}
	s.session = session
	return nil
}

func TestMaintain(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected failure")
	tests := []struct {
		name                                      string
		failure                                   string
		failSave                                  int
		due, missing, mismatch, pending, canceled bool
		want                                      []string
		wantErr                                   error
		active, saved, stillPending               bool
	}{
		{name: "not due", want: []string{"check"}},
		{name: "renew", due: true, want: []string{"check", "refresh", "validate", "save", "activate", "confirm", "save"}, active: true, saved: true},
		{name: "missing token", missing: true, wantErr: ErrMissingCredentials},
		{name: "check failure", failure: "check", want: []string{"check"}, wantErr: failure},
		{name: "refresh failure", due: true, failure: "refresh", want: []string{"check", "refresh"}, wantErr: failure},
		{name: "validation failure", due: true, failure: "validate", want: []string{"check", "refresh", "validate"}, wantErr: failure},
		{name: "wrong account", due: true, mismatch: true, want: []string{"check", "refresh", "validate"}, wantErr: ErrIdentityChanged},
		{name: "save failure", due: true, failSave: 1, want: []string{"check", "refresh", "validate", "save"}, wantErr: failure},
		{name: "confirm failure", due: true, failure: "confirm", want: []string{"check", "refresh", "validate", "save", "activate", "confirm"}, wantErr: failure, active: true, saved: true, stillPending: true},
		{name: "confirm save failure", due: true, failSave: 2, want: []string{"check", "refresh", "validate", "save", "activate", "confirm", "save"}, wantErr: failure, active: true, saved: true, stillPending: true},
		{name: "resume confirmation", pending: true, want: []string{"confirm", "save"}, saved: true},
		{name: "resume confirmation failure", pending: true, failure: "confirm", want: []string{"confirm"}, wantErr: failure, saved: true, stillPending: true},
		{name: "canceled", canceled: true, wantErr: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls []string
			current := model.BiliSession{AccountUID: "42", Cookies: map[string]string{"SESSDATA": "old", "bili_jct": "old-csrf"}, RefreshToken: "old-token"}
			if tt.missing {
				current.RefreshToken = ""
			}
			if tt.pending {
				current.Cookies = map[string]string{"SESSDATA": "new", "bili_jct": "new-csrf"}
				current.RefreshToken = "new-token"
				current.PendingRefreshToken = "old-token"
			}
			store := &fakeStore{session: current, calls: &calls, failAt: tt.failSave, err: failure}
			protocol := fakeProtocol{calls: &calls, failure: tt.failure, err: failure, due: tt.due, uid: "42"}
			if tt.mismatch {
				protocol.uid = "99"
			}
			activated := false
			now := time.Unix(1000, 0)
			manager := Manager{Protocol: protocol, Store: store, Now: func() time.Time { return now }, Activate: func(s model.BiliSession) {
				calls = append(calls, "activate")
				activated = true
				assert.Equal(t, store.session, s)
				assert.Equal(t, now, s.UpdatedAt)
			}}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tt.canceled {
				cancel()
			}
			err := manager.Maintain(ctx, current)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, calls)
			assert.Equal(t, tt.active, activated)
			assert.Equal(t, tt.saved, store.session.RefreshToken == "new-token")
			assert.Equal(t, tt.stillPending, store.session.PendingRefreshToken != "")
		})
	}
}

func TestRunScheduling(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	ticks := make(chan time.Time)
	wake := make(chan struct{})
	calls := make(chan struct{}, 3)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, ticks, wake, func(context.Context) { calls <- struct{}{} }) }()
	await := func() {
		t.Helper()
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatal("maintenance not scheduled")
		}
	}
	await()
	ticks <- time.Now()
	await()
	wake <- struct{}{}
	await()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop")
	}
	assert.Empty(t, calls)
}
