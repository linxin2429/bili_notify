package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Platform identifies an upstream content system. It is deliberately part of
// every persistent identity: upstream numeric identifiers are only unique
// within one platform.
type Platform string

const (
	PlatformBilibili Platform = "bilibili"
	PlatformZSXQ     Platform = "zsxq"
)

func (p Platform) Validate() error {
	switch p {
	case PlatformBilibili, PlatformZSXQ:
		return nil
	default:
		return fmt.Errorf("unsupported platform %q", p)
	}
}

type AccountStatus string

const (
	AccountDisconnected AccountStatus = "disconnected"
	AccountConnected    AccountStatus = "connected"
	AccountInvalid      AccountStatus = "invalid"
	AccountRiskPaused   AccountStatus = "risk_paused"
)

// PlatformAccount is the non-secret account projection. Session contains the
// opaque cookie values only inside the service/store boundary and is excluded
// from JSON responses.
type PlatformAccount struct {
	Platform        Platform          `json:"platform"`
	ExternalID      string            `json:"external_id,omitempty"`
	DisplayName     string            `json:"display_name,omitempty"`
	MaskedPhone     string            `json:"masked_phone,omitempty"`
	Status          AccountStatus     `json:"status"`
	Session         map[string]string `json:"-"`
	VerifiedAt      time.Time         `json:"verified_at,omitzero"`
	UpdatedAt       time.Time         `json:"updated_at,omitzero"`
	RiskPausedUntil time.Time         `json:"risk_paused_until,omitzero"`
	LastError       string            `json:"last_error,omitempty"`
}

func (a PlatformAccount) Validate() error {
	if err := a.Platform.Validate(); err != nil {
		return err
	}
	if a.Status == AccountConnected && strings.TrimSpace(a.ExternalID) == "" {
		return errors.New("connected account external_id is required")
	}
	return nil
}

type SourceType string

const (
	SourceBilibiliUP SourceType = "up"
	SourceZSXQPlanet SourceType = "planet"
)

type BaselineState string

const (
	BaselinePending  BaselineState = "pending"
	BaselineRunning  BaselineState = "running"
	BaselineComplete BaselineState = "complete"
	BaselineFailed   BaselineState = "failed"
)

type ZSXQTopicMode string

const (
	ZSXQTopicAll             ZSXQTopicMode = "all"
	ZSXQTopicSelectedAuthors ZSXQTopicMode = "selected_authors"
)

type ZSXQAuthor struct {
	UserID string `json:"user_id"`
	Name   string `json:"name,omitempty"`
}

// Source is one administrator-selectable collection boundary.
type Source struct {
	ID               string        `json:"id"`
	Platform         Platform      `json:"platform"`
	Type             SourceType    `json:"type"`
	ExternalID       string        `json:"external_id"`
	Name             string        `json:"name"`
	Note             string        `json:"note,omitempty"`
	OwnerID          string        `json:"owner_id,omitempty"`
	OwnerName        string        `json:"owner_name,omitempty"`
	ZSXQTopicMode    ZSXQTopicMode `json:"zsxq_topic_mode,omitempty"`
	ZSXQAuthors      []ZSXQAuthor  `json:"zsxq_authors,omitempty"`
	Enabled          bool          `json:"enabled"`
	BaselineState    BaselineState `json:"baseline_state"`
	BackfillCursor   string        `json:"backfill_cursor,omitempty"`
	HighWatermark    string        `json:"high_watermark,omitempty"`
	BackfillDone     int64         `json:"backfill_done"`
	BackfillTotal    int64         `json:"backfill_total"`
	LastPollAt       time.Time     `json:"last_poll_at,omitzero"`
	LastSuccessAt    time.Time     `json:"last_success_at,omitzero"`
	LastCommentAt    time.Time     `json:"last_comment_at,omitzero"`
	SyncLagSec       int64         `json:"sync_lag_sec"`
	LastError        string        `json:"last_error,omitempty"`
	ConsecutiveFails int           `json:"consecutive_fails"`
}

func SourceID(platform Platform, externalID string) string {
	externalID = strings.TrimSpace(externalID)
	switch platform {
	case PlatformBilibili:
		return "bilibili:up:" + externalID
	case PlatformZSXQ:
		return "zsxq:planet:" + externalID
	default:
		return string(platform) + ":source:" + externalID
	}
}

func (s Source) Validate() error {
	if err := s.Platform.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.ExternalID) == "" {
		return errors.New("source external_id is required")
	}
	expectedType := SourceBilibiliUP
	if s.Platform == PlatformZSXQ {
		expectedType = SourceZSXQPlanet
	}
	if s.Type != expectedType {
		return fmt.Errorf("source type %q is invalid for platform %q", s.Type, s.Platform)
	}
	if s.ID != SourceID(s.Platform, s.ExternalID) {
		return fmt.Errorf("source id must be %q", SourceID(s.Platform, s.ExternalID))
	}
	if s.Platform != PlatformZSXQ {
		if s.ZSXQTopicMode != "" || len(s.ZSXQAuthors) != 0 {
			return errors.New("bilibili source cannot have Knowledge Planet topic filters")
		}
		return nil
	}
	if s.ZSXQTopicMode != ZSXQTopicAll && s.ZSXQTopicMode != ZSXQTopicSelectedAuthors {
		return errors.New("Knowledge Planet topic mode must be all or selected_authors")
	}
	if s.ZSXQTopicMode == ZSXQTopicAll && len(s.ZSXQAuthors) != 0 {
		return errors.New("Knowledge Planet all topic mode cannot have selected authors")
	}
	if s.ZSXQTopicMode == ZSXQTopicSelectedAuthors && len(s.ZSXQAuthors) == 0 {
		return errors.New("Knowledge Planet selected author mode requires at least one author")
	}
	seenAuthors := make(map[string]struct{}, len(s.ZSXQAuthors))
	for _, author := range s.ZSXQAuthors {
		userID := strings.TrimSpace(author.UserID)
		if userID == "" || userID != author.UserID || strings.Trim(userID, "0123456789") != "" || userID[0] == '0' {
			return fmt.Errorf("invalid Knowledge Planet author user_id %q", author.UserID)
		}
		if _, exists := seenAuthors[userID]; exists {
			return fmt.Errorf("duplicate Knowledge Planet author user_id %q", userID)
		}
		seenAuthors[userID] = struct{}{}
	}
	return nil
}

type ContentType string

const (
	ContentDynamic     ContentType = "dynamic"
	ContentVideo       ContentType = "video"
	ContentArticle     ContentType = "article"
	ContentDiscussion  ContentType = "discussion"
	ContentQuestion    ContentType = "question"
	ContentAnswer      ContentType = "answer"
	ContentTask        ContentType = "task"
	ContentLongArticle ContentType = "long_article"
)

type Content struct {
	ID             string           `json:"id"`
	Platform       Platform         `json:"platform"`
	SourceID       string           `json:"source_id"`
	ExternalID     string           `json:"external_id"`
	AuthorID       string           `json:"author_id,omitempty"`
	AuthorName     string           `json:"author_name,omitempty"`
	UpstreamType   string           `json:"upstream_type"`
	Type           ContentType      `json:"type"`
	Title          string           `json:"title,omitempty"`
	Text           string           `json:"text,omitempty"`
	SafeHTML       string           `json:"safe_html,omitempty"`
	URL            string           `json:"url,omitempty"`
	PublishedAt    time.Time        `json:"published_at"`
	UpdatedAt      time.Time        `json:"updated_at,omitzero"`
	FirstSeenAt    time.Time        `json:"first_seen_at"`
	LastSyncedAt   time.Time        `json:"last_synced_at"`
	DeletedAt      time.Time        `json:"deleted_at,omitzero"`
	Stats          map[string]int64 `json:"stats,omitempty"`
	TreeIncomplete bool             `json:"tree_incomplete,omitempty"`
	Baseline       bool             `json:"baseline"`
}

func ContentID(platform Platform, externalID string) string {
	return string(platform) + ":content:" + strings.TrimSpace(externalID)
}

func (c Content) Validate() error {
	if err := c.Platform.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.ExternalID) == "" || strings.TrimSpace(c.SourceID) == "" {
		return errors.New("content external_id and source_id are required")
	}
	if c.ID != ContentID(c.Platform, c.ExternalID) {
		return fmt.Errorf("content id must be %q", ContentID(c.Platform, c.ExternalID))
	}
	if strings.TrimSpace(c.UpstreamType) == "" || c.Type == "" {
		return errors.New("content upstream_type and type are required")
	}
	return nil
}

type AttachmentType string

const (
	AttachmentImage AttachmentType = "image"
	AttachmentFile  AttachmentType = "file"
	AttachmentAudio AttachmentType = "audio"
	AttachmentVideo AttachmentType = "video"
	AttachmentLink  AttachmentType = "link"
)

type Attachment struct {
	ID            string         `json:"id"`
	ContentID     string         `json:"content_id"`
	ExternalID    string         `json:"external_id"`
	Type          AttachmentType `json:"type"`
	FileName      string         `json:"file_name,omitempty"`
	MIME          string         `json:"mime,omitempty"`
	Size          int64          `json:"size,omitempty"`
	Width         int            `json:"width,omitempty"`
	Height        int            `json:"height,omitempty"`
	DurationSec   int64          `json:"duration_sec,omitempty"`
	RemoteURL     string         `json:"-"`
	RemoteHost    string         `json:"remote_host,omitempty"`
	LocalPath     string         `json:"local_path,omitempty"`
	LocalizeError string         `json:"localize_error,omitempty"`
}

type AuthorRole string

const (
	RoleOwner   AuthorRole = "owner"
	RoleAdmin   AuthorRole = "admin"
	RoleGuest   AuthorRole = "guest"
	RolePartner AuthorRole = "partner"
	RoleMember  AuthorRole = "member"
	RoleUP      AuthorRole = "up"
)

func CommentID(platform Platform, externalID string) string {
	return string(platform) + ":comment:" + strings.TrimSpace(externalID)
}

type CommentPath struct {
	TriggerID string        `json:"trigger_id"`
	Nodes     []CommentNode `json:"nodes"`
}

type CommentDigest struct {
	Platform   Platform      `json:"platform"`
	ContentID  string        `json:"content_id"`
	SourceID   string        `json:"source_id"`
	BatchID    string        `json:"batch_id"`
	Title      string        `json:"title,omitempty"`
	ContentURL string        `json:"content_url,omitempty"`
	Incomplete bool          `json:"incomplete,omitempty"`
	Triggers   []CommentNode `json:"triggers"`
	Paths      []CommentPath `json:"paths"`
}

// DigestKey is stable regardless of the order in which pages returned comments.
func (d CommentDigest) DigestKey() string {
	ids := make([]string, 0, len(d.Triggers))
	for _, trigger := range d.Triggers {
		id := trigger.ID
		if id == "" {
			id = trigger.RPID
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return string(d.Platform) + ":" + d.ContentID + ":" + strings.Join(ids, ",")
}
