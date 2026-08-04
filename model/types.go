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
	UID             string    `json:"uid"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	BaselineReady   bool      `json:"baseline_ready"`
	LastPollAt      time.Time `json:"last_poll_at,omitzero"`
	LastSuccessAt   time.Time `json:"last_success_at,omitzero"`
	LastError       string    `json:"last_error,omitempty"`
	ConsecutiveFail int       `json:"consecutive_fail"`
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

type Delivery struct {
	ID        string        `json:"id"`
	Dynamic   Dynamic       `json:"dynamic"`
	ChannelID string        `json:"channel_id"`
	State     DeliveryState `json:"state"`
	Attempts  int           `json:"attempts"`
	NextAt    time.Time     `json:"next_at"`
	LastError string        `json:"last_error,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

type BiliSession struct {
	Cookies   map[string]string `json:"cookies"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding model: %w", err)
	}
	return b, nil
}
