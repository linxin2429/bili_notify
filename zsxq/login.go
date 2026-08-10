package zsxq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/linxin2429/bili_notify/model"
)

const (
	LoginTransactionTTL = 10 * time.Minute
	SMSResendInterval   = 120 * time.Second
	MaxCodeAttempts     = 5
)

var (
	ErrSMSCooldown      = errors.New("SMS code can only be resent after 120 seconds")
	ErrLoginNotFound    = errors.New("zsxq login transaction not found")
	ErrLoginExpired     = errors.New("zsxq login transaction expired")
	ErrAttemptsExceeded = errors.New("zsxq SMS code attempts exceeded")
)

type AccountStore interface {
	PutPlatformAccount(model.PlatformAccount) error
	MergeVisibleSources(model.Platform, []model.Source) error
}

type LoginTransaction struct {
	ID           string    `json:"id"`
	MaskedPhone  string    `json:"masked_phone"`
	ExpiresAt    time.Time `json:"expires_at"`
	NextSendAt   time.Time `json:"next_send_at"`
	AttemptsLeft int       `json:"attempts_left"`

	countryCode string
	phone       string
	attempts    int
}

type LoginManager struct {
	mu           sync.Mutex
	client       *Client
	store        AccountStore
	now          func() time.Time
	transactions map[string]*LoginTransaction
	lastSent     map[string]time.Time
}

func NewLoginManager(client *Client, store AccountStore) (*LoginManager, error) {
	if client == nil || store == nil {
		return nil, errors.New("zsxq client and account store are required")
	}
	return &LoginManager{client: client, store: store, now: time.Now, transactions: make(map[string]*LoginTransaction), lastSent: make(map[string]time.Time)}, nil
}

func (manager *LoginManager) SetClockForTest(clock func() time.Time) {
	manager.mu.Lock()
	manager.now = clock
	manager.mu.Unlock()
}

func (manager *LoginManager) SendCode(ctx context.Context, request SMSCodeRequest) (LoginTransaction, error) {
	if err := request.Validate(); err != nil {
		return LoginTransaction{}, err
	}
	key := request.CountryCode + request.Phone
	manager.mu.Lock()
	now := manager.now()
	if last := manager.lastSent[key]; !last.IsZero() && now.Before(last.Add(SMSResendInterval)) {
		manager.mu.Unlock()
		return LoginTransaction{}, ErrSMSCooldown
	}
	// Reserve the cooldown before the network call so concurrent requests cannot
	// both send an SMS. Release it if upstream did not accept the request.
	manager.lastSent[key] = now
	manager.mu.Unlock()
	if err := manager.client.SendSMSCode(ctx, request); err != nil {
		manager.mu.Lock()
		if manager.lastSent[key] == now {
			delete(manager.lastSent, key)
		}
		manager.mu.Unlock()
		return LoginTransaction{}, err
	}
	id, err := randomID()
	if err != nil {
		return LoginTransaction{}, err
	}
	transaction := &LoginTransaction{ID: id, MaskedPhone: maskPhone(request.CountryCode, request.Phone), ExpiresAt: now.Add(LoginTransactionTTL),
		NextSendAt: now.Add(SMSResendInterval), AttemptsLeft: MaxCodeAttempts, countryCode: request.CountryCode, phone: request.Phone}
	manager.mu.Lock()
	manager.pruneLocked(now)
	manager.transactions[id] = transaction
	manager.mu.Unlock()
	return transaction.public(), nil
}

func (manager *LoginManager) SubmitCode(ctx context.Context, transactionID, code string) (model.PlatformAccount, error) {
	manager.mu.Lock()
	now := manager.now()
	transaction := manager.transactions[transactionID]
	if transaction == nil {
		manager.mu.Unlock()
		return model.PlatformAccount{}, ErrLoginNotFound
	}
	if !now.Before(transaction.ExpiresAt) {
		delete(manager.transactions, transactionID)
		manager.mu.Unlock()
		return model.PlatformAccount{}, ErrLoginExpired
	}
	if transaction.attempts >= MaxCodeAttempts {
		delete(manager.transactions, transactionID)
		manager.mu.Unlock()
		return model.PlatformAccount{}, ErrAttemptsExceeded
	}
	transaction.attempts++
	transaction.AttemptsLeft = MaxCodeAttempts - transaction.attempts
	countryCode, phone := transaction.countryCode, transaction.phone
	manager.mu.Unlock()

	result, err := manager.client.Login(ctx, countryCode, phone, strings.TrimSpace(code))
	if err != nil {
		return model.PlatformAccount{}, err
	}
	account := model.PlatformAccount{Platform: model.PlatformZSXQ, ExternalID: result.AccountID, DisplayName: result.AccountName,
		MaskedPhone: maskPhone(countryCode, phone), Status: model.AccountConnected, Session: result.Cookies, VerifiedAt: now, UpdatedAt: now}
	if err := manager.store.PutPlatformAccount(account); err != nil {
		return model.PlatformAccount{}, err
	}
	if _, err := manager.SyncSources(ctx); err != nil {
		return model.PlatformAccount{}, err
	}
	manager.mu.Lock()
	delete(manager.transactions, transactionID)
	manager.mu.Unlock()
	account.Session = nil
	return account, nil
}

func (manager *LoginManager) SyncSources(ctx context.Context) ([]model.Source, error) {
	groups, err := manager.client.Groups(ctx)
	if err != nil {
		return nil, err
	}
	sources := make([]model.Source, 0, len(groups))
	for _, group := range groups {
		sources = append(sources, model.Source{ID: model.SourceID(model.PlatformZSXQ, group.ID), Platform: model.PlatformZSXQ,
			Type: model.SourceZSXQPlanet, ExternalID: group.ID, Name: group.Name, OwnerID: group.OwnerID, OwnerName: group.OwnerName,
			BaselineState: model.BaselinePending})
	}
	if err := manager.store.MergeVisibleSources(model.PlatformZSXQ, sources); err != nil {
		return nil, err
	}
	return sources, nil
}

func (manager *LoginManager) ClearSession() {
	manager.client.ClearSession()
	manager.mu.Lock()
	manager.transactions = make(map[string]*LoginTransaction)
	manager.lastSent = make(map[string]time.Time)
	manager.mu.Unlock()
}

func (manager *LoginManager) pruneLocked(now time.Time) {
	for id, transaction := range manager.transactions {
		if !now.Before(transaction.ExpiresAt) {
			delete(manager.transactions, id)
		}
	}
	for phone, sentAt := range manager.lastSent {
		if !now.Before(sentAt.Add(SMSResendInterval)) {
			delete(manager.lastSent, phone)
		}
	}
}

func (transaction LoginTransaction) public() LoginTransaction {
	transaction.countryCode = ""
	transaction.phone = ""
	return transaction
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func maskPhone(countryCode, phone string) string {
	phone = strings.TrimSpace(phone)
	if len(phone) <= 4 {
		return countryCode + strings.Repeat("*", len(phone))
	}
	visiblePrefix := min(3, len(phone)-4)
	return countryCode + " " + phone[:visiblePrefix] + strings.Repeat("*", len(phone)-visiblePrefix-4) + phone[len(phone)-4:]
}
