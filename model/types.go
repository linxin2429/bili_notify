package model

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type UP struct {
	UID             string          `json:"uid"`
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	BaselineReady   bool            `json:"baseline_ready"`
	FollowState     FollowState     `json:"follow_state"`
	FollowCheckedAt time.Time       `json:"follow_checked_at,omitzero"`
	CollectionRoute CollectionRoute `json:"collection_route"`
	// ExclusiveBaselineReady prevents pre-upgrade paid dynamics from being delivered.
	ExclusiveBaselineReady bool      `json:"-"`
	LastPollAt             time.Time `json:"last_poll_at,omitzero"`
	LastSuccessAt          time.Time `json:"last_success_at,omitzero"`
	LastError              string    `json:"last_error,omitempty"`
	ConsecutiveFail        int       `json:"consecutive_fail"`
}

type FollowState string

const (
	FollowUnknown    FollowState = "unknown"
	Followed         FollowState = "followed"
	FollowUnfollowed FollowState = "unfollowed"
)

type CollectionRoute string

const (
	CollectionRouteSpace   CollectionRoute = "space"
	CollectionRouteFeedAll CollectionRoute = "feed_all"
)

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
		if c.Type == ChannelFeishu {
			appID := strings.TrimSpace(s["app_id"])
			appSecret := strings.TrimSpace(s["app_secret"])
			if (appID == "") != (appSecret == "") {
				return errors.New("feishu app_id and app_secret must both be set or both empty")
			}
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
	Kind        DynamicMediaKind `json:"kind"`
	URL         string           `json:"url"`
	Width       int              `json:"width,omitempty"`
	Height      int              `json:"height,omitempty"`
	LocalPath   string           `json:"local_path,omitempty"` // relative to data_dir
	ContentType string           `json:"content_type,omitempty"`
	Size        int64            `json:"size,omitempty"`
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
	// Progress tracks multi-part channel sends (e.g. WeCom text + images).
	Progress *DeliveryProgress `json:"progress,omitempty"`
}

// DeliveryProgress records which parts of a multi-message delivery already succeeded.
type DeliveryProgress struct {
	TextSent   bool `json:"text_sent,omitempty"`
	ImagesSent int  `json:"images_sent,omitempty"`
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
	Cookies     map[string]string `json:"cookies"`
	AccountUID  string            `json:"account_uid"`
	AccountName string            `json:"account_name"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type BiliAccount struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
}

// Runtime settings bounds and product defaults shared by startup seeding,
// persistence, the service engine, and the management API.
const (
	MinPollIntervalSec         = 10
	MaxPollIntervalSec         = 24 * 60 * 60
	MaxRequestRate             = 10.0
	MinRequestConcurrency      = 1
	MaxRequestConcurrency      = 16
	MinCommentBatchIntervalSec = 30
	MaxCommentBatchIntervalSec = 24 * 60 * 60
	MaxCommentTrackN           = 50
	MaxCommentRootPages        = 10
	MaxCommentReplyPages       = 20
	MinLogRetentionDays        = 1
	MaxLogRetentionDays        = 3650
	MinRelationRefreshSec      = 60
	MaxRelationRefreshSec      = 24 * 60 * 60
	MinSpaceReconcileSec       = 5 * 60
	MaxSpaceReconcileSec       = 7 * 24 * 60 * 60
	MinDynamicPages            = 1
	MaxDynamicPages            = 20
	MinRiskPauseSec            = 60
	MaxRiskPauseSec            = 60 * 60
	MinDeliveryConcurrency     = 1
	MaxDeliveryConcurrency     = 32
	MinBacklogAlertCount       = 1
	MaxBacklogAlertCount       = 100000
	MinBacklogAlertAgeSec      = 60
	MaxBacklogAlertAgeSec      = 24 * 60 * 60
	MinDeliveryRetryDelaySec   = 1
	MaxDeliveryRetryDelaySec   = 24 * 60 * 60
	DeliveryRetryStages        = 5

	DefaultPollIntervalSec     = 30
	DefaultRequestRate         = 2.0
	DefaultRequestConcurrency  = 4
	DefaultCommentTrackN       = 10
	DefaultCommentRootPages    = 2
	DefaultCommentReplyPages   = 5
	DefaultCommentBatchSec     = 120
	DefaultLogLevel            = "info"
	DefaultAuditRetentionDays  = 180
	DefaultRelationRefreshSec  = 10 * 60
	DefaultSpaceReconcileSec   = 30 * 60
	DefaultMaxDynamicPages     = 10
	DefaultRiskPauseSec        = 5 * 60
	DefaultDeliveryConcurrency = 8
	DefaultBacklogAlertCount   = 100
	DefaultBacklogAlertAgeSec  = 5 * 60
)

// DeliveryRetryDelays stores the five retry-stage upper bounds in seconds.
type DeliveryRetryDelays [DeliveryRetryStages]int

// RuntimeSettings is the complete hot-reloadable configuration persisted in the store.
type RuntimeSettings struct {
	PollIntervalSec         int                 `json:"poll_interval_sec"`
	RequestRate             float64             `json:"request_rate"`
	RequestConcurrency      int                 `json:"request_concurrency"`
	CommentEnabled          bool                `json:"comment_enabled"`
	CommentTrackN           int                 `json:"comment_track_n"`
	CommentRootPages        int                 `json:"comment_root_pages"`
	CommentReplyPages       int                 `json:"comment_reply_pages"`
	CommentBatchIntervalSec int                 `json:"comment_batch_interval_sec"`
	LogLevel                string              `json:"log_level"`
	AuditLogRetentionDays   int                 `json:"audit_log_retention_days"`
	RelationRefreshSec      int                 `json:"relation_refresh_interval_sec"`
	SpaceReconcileSec       int                 `json:"space_reconcile_interval_sec"`
	MaxDynamicPages         int                 `json:"max_dynamic_pages"`
	RiskPauseSec            int                 `json:"risk_pause_sec"`
	DeliveryConcurrency     int                 `json:"delivery_concurrency"`
	BacklogAlertCount       int                 `json:"backlog_alert_count"`
	BacklogAlertAgeSec      int                 `json:"backlog_alert_age_sec"`
	DeliveryRetryDelaysSec  DeliveryRetryDelays `json:"delivery_retry_delays_sec"`
}

func (s RuntimeSettings) PollInterval() time.Duration {
	return time.Duration(s.PollIntervalSec) * time.Second
}

func (s RuntimeSettings) CommentBatchInterval() time.Duration {
	return time.Duration(s.CommentBatchIntervalSec) * time.Second
}

func (s RuntimeSettings) RelationRefreshInterval() time.Duration {
	return time.Duration(s.RelationRefreshSec) * time.Second
}

func (s RuntimeSettings) SpaceReconcileInterval() time.Duration {
	return time.Duration(s.SpaceReconcileSec) * time.Second
}

func (s RuntimeSettings) RiskPause() time.Duration {
	return time.Duration(s.RiskPauseSec) * time.Second
}

func (s RuntimeSettings) BacklogAlertAge() time.Duration {
	return time.Duration(s.BacklogAlertAgeSec) * time.Second
}

func (s RuntimeSettings) AuditLogRetention() time.Duration {
	return time.Duration(s.AuditLogRetentionDays) * 24 * time.Hour
}

func DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{
		PollIntervalSec: DefaultPollIntervalSec, RequestRate: DefaultRequestRate, RequestConcurrency: DefaultRequestConcurrency,
		CommentEnabled: true, CommentTrackN: DefaultCommentTrackN, CommentRootPages: DefaultCommentRootPages,
		CommentReplyPages: DefaultCommentReplyPages, CommentBatchIntervalSec: DefaultCommentBatchSec,
		LogLevel: DefaultLogLevel, AuditLogRetentionDays: DefaultAuditRetentionDays,
		RelationRefreshSec: DefaultRelationRefreshSec, SpaceReconcileSec: DefaultSpaceReconcileSec, MaxDynamicPages: DefaultMaxDynamicPages,
		RiskPauseSec: DefaultRiskPauseSec, DeliveryConcurrency: DefaultDeliveryConcurrency,
		BacklogAlertCount: DefaultBacklogAlertCount, BacklogAlertAgeSec: DefaultBacklogAlertAgeSec,
		DeliveryRetryDelaysSec: DeliveryRetryDelays{5, 30, 120, 600, 3600},
	}
}

func (s RuntimeSettings) Validate() error {
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
		errs = append(errs, fmt.Errorf("comment_batch_interval_sec must be in [%d, %d]", MinCommentBatchIntervalSec, MaxCommentBatchIntervalSec))
	} else if s.CommentBatchIntervalSec > MaxCommentBatchIntervalSec {
		errs = append(errs, fmt.Errorf("comment_batch_interval_sec must be in [%d, %d]", MinCommentBatchIntervalSec, MaxCommentBatchIntervalSec))
	}
	if s.LogLevel != "debug" && s.LogLevel != "info" && s.LogLevel != "warn" && s.LogLevel != "error" {
		errs = append(errs, errors.New("log_level must be debug, info, warn, or error"))
	}
	if s.AuditLogRetentionDays < MinLogRetentionDays || s.AuditLogRetentionDays > MaxLogRetentionDays {
		errs = append(errs, fmt.Errorf("audit_log_retention_days must be in [%d, %d]", MinLogRetentionDays, MaxLogRetentionDays))
	}
	if s.RelationRefreshSec < MinRelationRefreshSec || s.RelationRefreshSec > MaxRelationRefreshSec {
		errs = append(errs, fmt.Errorf("relation_refresh_interval_sec must be in [%d, %d]", MinRelationRefreshSec, MaxRelationRefreshSec))
	}
	if s.SpaceReconcileSec < MinSpaceReconcileSec || s.SpaceReconcileSec > MaxSpaceReconcileSec {
		errs = append(errs, fmt.Errorf("space_reconcile_interval_sec must be in [%d, %d]", MinSpaceReconcileSec, MaxSpaceReconcileSec))
	}
	if s.MaxDynamicPages < MinDynamicPages || s.MaxDynamicPages > MaxDynamicPages {
		errs = append(errs, fmt.Errorf("max_dynamic_pages must be in [%d, %d]", MinDynamicPages, MaxDynamicPages))
	}
	if s.RiskPauseSec < MinRiskPauseSec || s.RiskPauseSec > MaxRiskPauseSec {
		errs = append(errs, fmt.Errorf("risk_pause_sec must be in [%d, %d]", MinRiskPauseSec, MaxRiskPauseSec))
	}
	if s.DeliveryConcurrency < MinDeliveryConcurrency || s.DeliveryConcurrency > MaxDeliveryConcurrency {
		errs = append(errs, fmt.Errorf("delivery_concurrency must be in [%d, %d]", MinDeliveryConcurrency, MaxDeliveryConcurrency))
	}
	if s.BacklogAlertCount < MinBacklogAlertCount || s.BacklogAlertCount > MaxBacklogAlertCount {
		errs = append(errs, fmt.Errorf("backlog_alert_count must be in [%d, %d]", MinBacklogAlertCount, MaxBacklogAlertCount))
	}
	if s.BacklogAlertAgeSec < MinBacklogAlertAgeSec || s.BacklogAlertAgeSec > MaxBacklogAlertAgeSec {
		errs = append(errs, fmt.Errorf("backlog_alert_age_sec must be in [%d, %d]", MinBacklogAlertAgeSec, MaxBacklogAlertAgeSec))
	}
	previous := 0
	for index, delay := range s.DeliveryRetryDelaysSec {
		if delay < MinDeliveryRetryDelaySec || delay > MaxDeliveryRetryDelaySec {
			errs = append(errs, fmt.Errorf("delivery_retry_delays_sec[%d] must be in [%d, %d]", index, MinDeliveryRetryDelaySec, MaxDeliveryRetryDelaySec))
		}
		if index > 0 && delay < previous {
			errs = append(errs, errors.New("delivery_retry_delays_sec must be nondecreasing"))
			break
		}
		previous = delay
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
	if pollInterval < time.Duration(MinPollIntervalSec)*time.Second || pollInterval > time.Duration(MaxPollIntervalSec)*time.Second {
		errs = append(errs, errors.New("poll interval must be between 10s and 24h"))
	}
	if math.IsNaN(requestRate) || math.IsInf(requestRate, 0) || requestRate <= 0 || requestRate > MaxRequestRate {
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
