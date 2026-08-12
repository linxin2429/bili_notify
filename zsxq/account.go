package zsxq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/linxin2429/bili_notify/model"
)

const APIKeyCredential = "zsxq_api_key"

var ErrInvalidAPIKey = errors.New("invalid Knowledge Planet API key")

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

func NormalizeAPIKey(raw string) (string, error) {
	apiKey := strings.TrimSpace(raw)
	if apiKey == "" || len(apiKey) > 8<<10 {
		return "", ErrInvalidAPIKey
	}
	for _, character := range apiKey {
		if unicode.IsControl(character) {
			return "", ErrInvalidAPIKey
		}
	}
	return apiKey, nil
}

func APIKey(account model.PlatformAccount) (string, error) {
	apiKey := strings.TrimSpace(account.Session[APIKeyCredential])
	if apiKey == "" {
		return "", ErrAuthentication
	}
	return apiKey, nil
}

func (manager *AccountManager) UpdateCredential(ctx context.Context, rawAPIKey string) (model.PlatformAccount, error) {
	apiKey, err := NormalizeAPIKey(rawAPIKey)
	if err != nil {
		return model.PlatformAccount{}, err
	}
	user, err := manager.client.Me(ctx, apiKey)
	if err != nil {
		return model.PlatformAccount{}, err
	}
	now := manager.now()
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: user.ID, DisplayName: user.Name,
		Status: model.AccountConnected, Session: map[string]string{APIKeyCredential: apiKey}, VerifiedAt: now, UpdatedAt: now}
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
	apiKey, err := APIKey(account)
	if err != nil {
		return nil, err
	}
	groups, err := manager.client.Groups(ctx, apiKey)
	if !errors.Is(err, ErrAuthentication) {
		return groups, err
	}
	account.Status = model.AccountInvalid
	account.Session = map[string]string{}
	account.LastError = "API key update required"
	account.UpdatedAt = manager.now()
	if storeErr := manager.store.PutPlatformAccount(account); storeErr != nil {
		return nil, fmt.Errorf("invalidating rejected Knowledge Planet credential: %w", storeErr)
	}
	return nil, err
}
