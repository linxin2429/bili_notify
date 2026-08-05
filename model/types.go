package model

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type UP struct {
	UID           string `json:"uid"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	BaselineReady bool   `json:"baseline_ready"`
	// ExclusiveBaselineReady prevents pre-upgrade paid dynamics from being delivered.
	ExclusiveBaselineReady bool      `json:"-"`
	LastPollAt             time.Time `json:"last_poll_at,omitzero"`
	LastSuccessAt          time.Time `json:"last_success_at,omitzero"`
	LastError              string    `json:"last_error,omitempty"`
	ConsecutiveFail        int       `json:"consecutive_fail"`
}

func (u UP) Validate() error {
	uid, err := strconv.ParseUint(u.UID, 10, 64)
	if err != nil || uid == 0 {
		return errors.New("uid must be a positive decimal string")
	}
	return nil
}

func (u UP) UIDCompare(other UP) int { return cmp.Compare(u.UID, other.UID) }

type ChannelType string

const (
	ChannelEmail     ChannelType = "email"
	ChannelMicrosoft ChannelType = "microsoft"
	ChannelDingTalk  ChannelType = "dingtalk"
	ChannelFeishu    ChannelType = "feishu"
	ChannelWeCom     ChannelType = "wecom"
)

type Channel struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      ChannelType       `json:"type"`
	Enabled   bool              `json:"enabled"`
	Settings  map[string]string `json:"settings,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func (c Channel) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("channel name is required")
	}
	s := c.Settings
	switch c.Type {
	case ChannelEmail:
		for _, key := range []string{"host", "port", "tls", "from", "to"} {
			if strings.TrimSpace(s[key]) == "" {
				return fmt.Errorf("email setting %q is required", key)
			}
		}
		if s["tls"] != "starttls" && s["tls"] != "tls" {
			return errors.New("email tls must be starttls or tls")
		}
		if _, err := strconv.Atoi(s["port"]); err != nil {
			return errors.New("email port must be an integer")
		}
		if _, err := mail.ParseAddress(s["from"]); err != nil {
			return fmt.Errorf("invalid email from: %w", err)
		}
		for recipient := range strings.SplitSeq(s["to"], ",") {
			if _, err := mail.ParseAddress(strings.TrimSpace(recipient)); err != nil {
				return fmt.Errorf("invalid email recipient: %w", err)
			}
		}
	case ChannelMicrosoft:
		if !isGUID(s["client_id"]) {
			return errors.New("microsoft client_id must be an application UUID")
		}
		tenant := strings.TrimSpace(s["tenant"])
		if tenant != "" && !isMicrosoftTenant(tenant) {
			return errors.New("microsoft tenant must be common, consumers, organizations, a tenant UUID, or a tenant domain")
		}
		if strings.TrimSpace(s["to"]) == "" {
			return errors.New("microsoft setting \"to\" is required")
		}
		for recipient := range strings.SplitSeq(s["to"], ",") {
			if _, err := mail.ParseAddress(strings.TrimSpace(recipient)); err != nil {
				return fmt.Errorf("invalid microsoft recipient: %w", err)
			}
		}
		if c.Enabled && s["refresh_token"] == "" {
			return errors.New("microsoft channel must be authorized before it is enabled")
		}
	case ChannelDingTalk, ChannelFeishu, ChannelWeCom:
		u, err := url.Parse(s["webhook"])
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("webhook must be an absolute HTTPS URL")
		}
		if (c.Type == ChannelDingTalk || c.Type == ChannelFeishu) && s["secret"] == "" {
			return errors.New("signed robot secret is required")
		}
	default:
		return fmt.Errorf("unsupported channel type %q", c.Type)
	}
	return nil
}

func isGUID(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		return false
	}
	for _, part := range parts {
		for _, r := range part {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

func isMicrosoftTenant(value string) bool {
	if value == "common" || value == "consumers" || value == "organizations" || isGUID(value) {
		return true
	}
	if len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' || r == '.') {
			return false
		}
	}
	return strings.Contains(value, ".")
}

func (c Channel) NameCompare(other Channel) int { return cmp.Compare(c.Name, other.Name) }

type Dynamic struct {
	ID          string         `json:"id"`
	UID         string         `json:"uid"`
	UPName      string         `json:"up_name"`
	Type        string         `json:"type"`
	PublishedAt time.Time      `json:"published_at"`
	Summary     string         `json:"summary"`
	URL         string         `json:"url"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	TargetURL   string         `json:"target_url,omitempty"`
	Badge       string         `json:"badge,omitempty"`
	Links       []DynamicLink  `json:"links,omitempty"`
	Media       []DynamicMedia `json:"media,omitempty"`
	Stats       *DynamicStats  `json:"stats,omitempty"`
	Video       *DynamicVideo  `json:"video,omitempty"`
	Original    *Dynamic       `json:"original,omitempty"`
	// Exclusive is internal collection metadata used to baseline paid dynamics.
	Exclusive bool `json:"-"`
	// Comment coordinates for UP-reply monitoring. Commentable is true only when
	// CommentType and CommentOID are known without guessing.
	Commentable  bool   `json:"commentable,omitempty"`
	CommentType  int    `json:"comment_type,omitempty"`
	CommentOID   string `json:"comment_oid,omitempty"`
	CommentCount int64  `json:"comment_count,omitempty"`
}

type DynamicLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type DynamicMediaKind string

const (
	DynamicMediaCover DynamicMediaKind = "cover"
	DynamicMediaImage DynamicMediaKind = "image"
)

type DynamicMedia struct {
	Kind   DynamicMediaKind `json:"kind"`
	URL    string           `json:"url"`
	Width  int              `json:"width,omitempty"`
	Height int              `json:"height,omitempty"`
}

type DynamicStats struct {
	Forwards int64 `json:"forwards"`
	Comments int64 `json:"comments"`
	Likes    int64 `json:"likes"`
}

type DynamicVideo struct {
	Duration string `json:"duration,omitempty"`
	Views    string `json:"views,omitempty"`
	Danmaku  string `json:"danmaku,omitempty"`
}

type DeliveryState string

const (
	DeliveryPending DeliveryState = "pending"
	DeliveryBlocked DeliveryState = "blocked"
)

type DeliveryKind string

const (
	DeliveryKindDynamic DeliveryKind = "dynamic"
	DeliveryKindComment DeliveryKind = "comment"
)

type Delivery struct {
	ID        string               `json:"id"`
	Kind      DeliveryKind         `json:"kind,omitempty"` // empty means dynamic for back-compat
	Dynamic   Dynamic              `json:"dynamic,omitzero"`
	Comment   *CommentNotification `json:"comment,omitempty"`
	ChannelID string               `json:"channel_id"`
	State     DeliveryState        `json:"state"`
	Attempts  int                  `json:"attempts"`
	NextAt    time.Time            `json:"next_at"`
	LastError string               `json:"last_error,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

func (d Delivery) EffectiveKind() DeliveryKind {
	if d.Kind == DeliveryKindComment {
		return DeliveryKindComment
	}
	return DeliveryKindDynamic
}

// CommentTarget is one UP-owned content area tracked for UP-reply discovery.
type CommentTarget struct {
	UID           string    `json:"uid"`
	UPName        string    `json:"up_name"`
	DynamicID     string    `json:"dynamic_id"`
	ContentType   string    `json:"content_type"` // DYNAMIC_TYPE_*
	Title         string    `json:"title,omitempty"`
	URL           string    `json:"url"`
	CommentType   int       `json:"comment_type"`
	CommentOID    string    `json:"comment_oid"`
	PublishedAt   time.Time `json:"published_at"`
	CommentCount  int64     `json:"comment_count"`
	Closed        bool      `json:"closed,omitempty"`
	BaselineReady bool      `json:"baseline_ready"`
	LastPollAt    time.Time `json:"last_poll_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
}

func (t CommentTarget) Key() string {
	return strconv.Itoa(t.CommentType) + ":" + t.CommentOID
}

// CommentNotification is the outbox payload for one UP reply under own content.
type CommentNotification struct {
	RPID         string        `json:"rpid"`
	UPUID        string        `json:"up_uid"`
	UPName       string        `json:"up_name"`
	ContentType  string        `json:"content_type"`
	ContentID    string        `json:"content_id"`
	ContentTitle string        `json:"content_title,omitempty"`
	ContentURL   string        `json:"content_url"`
	PublishedAt  time.Time     `json:"published_at"`
	Incomplete   bool          `json:"incomplete,omitempty"`
	Thread       []CommentNode `json:"thread"`
}

type CommentNode struct {
	RPID      string    `json:"rpid"`
	Parent    string    `json:"parent,omitempty"`
	Mid       string    `json:"mid"`
	Name      string    `json:"name"`
	Message   string    `json:"message"`
	Time      time.Time `json:"time"`
	IsUP      bool      `json:"is_up,omitempty"`
	IsTrigger bool      `json:"is_trigger,omitempty"`
}

type BiliSession struct {
	Cookies   map[string]string `json:"cookies"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Collector parameter bounds shared by startup config and runtime settings.
const (
	MinPollIntervalSec         = 10
	MaxRequestRate             = 10.0
	MinRequestConcurrency      = 1
	MaxRequestConcurrency      = 16
	MinCommentBatchIntervalSec = 30
	MaxCommentTrackN           = 50
	MaxCommentRootPages        = 10
	MaxCommentReplyPages       = 20
	DefaultCommentTrackN       = 10
	DefaultCommentRootPages    = 2
	DefaultCommentReplyPages   = 5
	DefaultCommentBatchSec     = 120
)

// RuntimeSettings is the hot-reloadable collector configuration persisted in the store.
type RuntimeSettings struct {
	PollIntervalSec         int     `json:"poll_interval_sec"`
	RequestRate             float64 `json:"request_rate"`
	RequestConcurrency      int     `json:"request_concurrency"`
	CommentEnabled          bool    `json:"comment_enabled"`
	CommentTrackN           int     `json:"comment_track_n"`
	CommentRootPages        int     `json:"comment_root_pages"`
	CommentReplyPages       int     `json:"comment_reply_pages"`
	CommentBatchIntervalSec int     `json:"comment_batch_interval_sec"`
}

func (s RuntimeSettings) PollInterval() time.Duration {
	return time.Duration(s.PollIntervalSec) * time.Second
}

func (s RuntimeSettings) CommentBatchInterval() time.Duration {
	return time.Duration(s.CommentBatchIntervalSec) * time.Second
}

// WithCommentDefaults fills zero comment knobs with product defaults so older
// three-field settings records remain loadable after the comment feature ships.
func (s RuntimeSettings) WithCommentDefaults() RuntimeSettings {
	trackN, rootPages, replyPages, batchSec, enabled := DefaultCommentSettings()
	legacy := s.CommentTrackN == 0 && s.CommentRootPages == 0 && s.CommentReplyPages == 0 && s.CommentBatchIntervalSec == 0
	if s.CommentTrackN == 0 {
		s.CommentTrackN = trackN
	}
	if s.CommentRootPages == 0 {
		s.CommentRootPages = rootPages
	}
	if s.CommentReplyPages == 0 {
		s.CommentReplyPages = replyPages
	}
	if s.CommentBatchIntervalSec == 0 {
		s.CommentBatchIntervalSec = batchSec
	}
	if legacy {
		// Pre-feature records omit comment fields (all zero); enable by default.
		// Explicit saves always set non-zero track/pages so they are not legacy.
		s.CommentEnabled = enabled
	}
	return s
}

func (s RuntimeSettings) Validate() error {
	s = s.WithCommentDefaults()
	var errs []error
	if err := ValidateCollectorParams(s.PollInterval(), s.RequestRate, s.RequestConcurrency); err != nil {
		errs = append(errs, err)
	}
	if s.CommentTrackN < 1 || s.CommentTrackN > MaxCommentTrackN {
		errs = append(errs, fmt.Errorf("comment_track_n must be in [1, %d]", MaxCommentTrackN))
	}
	if s.CommentRootPages < 1 || s.CommentRootPages > MaxCommentRootPages {
		errs = append(errs, fmt.Errorf("comment_root_pages must be in [1, %d]", MaxCommentRootPages))
	}
	if s.CommentReplyPages < 1 || s.CommentReplyPages > MaxCommentReplyPages {
		errs = append(errs, fmt.Errorf("comment_reply_pages must be in [1, %d]", MaxCommentReplyPages))
	}
	if s.CommentBatchIntervalSec < MinCommentBatchIntervalSec {
		errs = append(errs, fmt.Errorf("comment_batch_interval_sec must be at least %d", MinCommentBatchIntervalSec))
	}
	return errors.Join(errs...)
}

// DefaultCommentSettings returns the comment-monitoring defaults used when seeding
// an empty store or filling missing fields on older settings records.
func DefaultCommentSettings() (trackN, rootPages, replyPages, batchSec int, enabled bool) {
	return DefaultCommentTrackN, DefaultCommentRootPages, DefaultCommentReplyPages, DefaultCommentBatchSec, true
}

// ValidateCollectorParams checks poll interval, request rate, and concurrency bounds.
func ValidateCollectorParams(pollInterval time.Duration, requestRate float64, concurrency int) error {
	var errs []error
	if pollInterval < time.Duration(MinPollIntervalSec)*time.Second {
		errs = append(errs, errors.New("poll interval must be at least 10s"))
	}
	if requestRate <= 0 || requestRate > MaxRequestRate {
		errs = append(errs, errors.New("request rate must be in (0, 10]"))
	}
	if concurrency < MinRequestConcurrency || concurrency > MaxRequestConcurrency {
		errs = append(errs, errors.New("request concurrency must be in [1, 16]"))
	}
	return errors.Join(errs...)
}

func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding model: %w", err)
	}
	return b, nil
}
