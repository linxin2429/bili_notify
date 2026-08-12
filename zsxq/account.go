package zsxq

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
)

const AccessTokenKey = "zsxq_access_token"

var ErrInvalidCookie = errors.New("invalid Knowledge Planet cookie")

type AccountStore interface {
	PlatformAccount(model.Platform) (model.PlatformAccount, error)
	PutPlatformAccount(model.PlatformAccount) error
}

type AccountManager struct {
	client *Client
	store  AccountStore
	now    func() time.Time
}

func NewAccountManager(client *Client, store AccountStore) (*AccountManager, error) {
	if client == nil || store == nil {
		return nil, errors.New("zsxq client and account store are required")
	}
	return &AccountManager{client: client, store: store, now: time.Now}, nil
}

func ParseAccessToken(rawCookie string) (string, error) {
	if strings.TrimSpace(rawCookie) == "" || strings.ContainsAny(rawCookie, "\r\n\x00") {
		return "", ErrInvalidCookie
	}
	cookies, err := http.ParseCookie(rawCookie)
	if err != nil {
		return "", ErrInvalidCookie
	}
	var token string
	for _, cookie := range cookies {
		if cookie.Name != AccessTokenKey {
			continue
		}
		if token != "" || strings.TrimSpace(cookie.Value) == "" {
			return "", ErrInvalidCookie
		}
		token = cookie.Value
	}
	if token == "" {
		return "", ErrInvalidCookie
	}
	return token, nil
}

func AccessToken(account model.PlatformAccount) (string, error) {
	token := strings.TrimSpace(account.Session[AccessTokenKey])
	if token == "" {
		return "", ErrAuthentication
	}
	return token, nil
}

func (manager *AccountManager) ImportCookie(ctx context.Context, rawCookie string) (model.PlatformAccount, error) {
	token, err := ParseAccessToken(rawCookie)
	if err != nil {
		return model.PlatformAccount{}, err
	}
	user, err := manager.client.Me(ctx, token)
	if err != nil {
		return model.PlatformAccount{}, err
	}
	now := manager.now()
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: user.ID, DisplayName: user.Name,
		Status: model.AccountConnected, Session: map[string]string{AccessTokenKey: token}, VerifiedAt: now, UpdatedAt: now}
	if err := manager.store.PutPlatformAccount(account); err != nil {
		return model.PlatformAccount{}, err
	}
	account.Session = nil
	return account, nil
}

func (manager *AccountManager) Groups(ctx context.Context) ([]Group, error) {
	account, err := manager.store.PlatformAccount(model.PlatformZSXQ)
	if err != nil {
		return nil, fmt.Errorf("loading Knowledge Planet account: %w", err)
	}
	if account.Status != model.AccountConnected {
		return nil, ErrAuthentication
	}
	token, err := AccessToken(account)
	if err != nil {
		return nil, err
	}
	groups, err := manager.client.Groups(ctx, token)
	if !errors.Is(err, ErrAuthentication) {
		return groups, err
	}
	account.Status = model.AccountInvalid
	account.Session = map[string]string{}
	account.LastError = "session import required"
	account.UpdatedAt = manager.now()
	if storeErr := manager.store.PutPlatformAccount(account); storeErr != nil {
		return nil, fmt.Errorf("invalidating rejected Knowledge Planet session: %w", storeErr)
	}
	return nil, err
}
