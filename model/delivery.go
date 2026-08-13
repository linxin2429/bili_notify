package model

import "time"

type DeliveryState string

const (
	DeliveryPending   DeliveryState = "pending"
	DeliveryBlocked   DeliveryState = "blocked"
	DeliverySuspended DeliveryState = "suspended"
)

type DeliveryKind string

const (
	DeliveryKindContent DeliveryKind = "content"
	DeliveryKindComment DeliveryKind = "comment"
	DeliveryKindAI      DeliveryKind = "ai"
	DeliveryKindSystem  DeliveryKind = "system"
)

// ContentSnapshot is the immutable, platform-neutral content payload sealed
// when an outbox row is created. Dispatch never rebuilds it from mutable rows.
type ContentSnapshot struct {
	Platform     Platform           `json:"platform"`
	SourceID     string             `json:"source_id"`
	SourceName   string             `json:"source_name,omitempty"`
	ContentID    string             `json:"content_id"`
	ExternalID   string             `json:"external_id"`
	AuthorID     string             `json:"author_id,omitempty"`
	AuthorName   string             `json:"author_name,omitempty"`
	Type         ContentType        `json:"type"`
	UpstreamType string             `json:"upstream_type"`
	Title        string             `json:"title,omitempty"`
	Text         string             `json:"text,omitempty"`
	Description  string             `json:"description,omitempty"`
	URL          string             `json:"url,omitempty"`
	TargetURL    string             `json:"target_url,omitempty"`
	Badge        string             `json:"badge,omitempty"`
	PublishedAt  time.Time          `json:"published_at"`
	Stats        map[string]int64   `json:"stats,omitempty"`
	Links        []SnapshotLink     `json:"links,omitempty"`
	Media        []SnapshotMedia    `json:"media,omitempty"`
	Files        []DeliveryFile     `json:"files,omitempty"`
	Video        *SnapshotVideoMeta `json:"video,omitempty"`
	ForwardOf    *ContentSnapshot   `json:"forward_of,omitempty"`
}

type SnapshotLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type SnapshotMedia struct {
	ID          string         `json:"id,omitempty"`
	Type        AttachmentType `json:"type"`
	Kind        string         `json:"kind,omitempty"`
	Name        string         `json:"name,omitempty"`
	URL         string         `json:"url,omitempty"`
	LocalPath   string         `json:"local_path,omitempty"`
	MIME        string         `json:"mime,omitempty"`
	Size        int64          `json:"size,omitempty"`
	Width       int            `json:"width,omitempty"`
	Height      int            `json:"height,omitempty"`
	DurationSec int64          `json:"duration_sec,omitempty"`
}

type SnapshotVideoMeta struct {
	BVID     string `json:"bvid,omitempty"`
	Duration string `json:"duration,omitempty"`
	Views    string `json:"views,omitempty"`
	Danmaku  string `json:"danmaku,omitempty"`
}

type SystemAlert struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// AINotification is the immutable payload queued when an AI stage reaches a
// terminal state. BVID is an optional Bilibili extension, not an identity.
type AINotification struct {
	JobID        string    `json:"job_id"`
	SourceID     string    `json:"source_id"`
	ContentID    string    `json:"content_id"`
	BVID         string    `json:"bvid,omitempty"`
	AuthorName   string    `json:"author_name,omitempty"`
	Title        string    `json:"title,omitempty"`
	Stage        AIJobKind `json:"stage"`
	Succeeded    bool      `json:"succeeded"`
	Body         string    `json:"body,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	SourceURL    string    `json:"source_url,omitempty"`
}

type Delivery struct {
	ID                string               `json:"id"`
	Kind              DeliveryKind         `json:"kind"`
	Content           *ContentSnapshot     `json:"content,omitempty"`
	Comment           *CommentNotification `json:"comment,omitempty"`
	AI                *AINotification      `json:"ai,omitempty"`
	System            *SystemAlert         `json:"system,omitempty"`
	ChannelID         string               `json:"channel_id"`
	State             DeliveryState        `json:"state"`
	Attempts          int                  `json:"attempts"`
	NextAt            time.Time            `json:"next_at"`
	LastError         string               `json:"last_error,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	Progress          *DeliveryProgress    `json:"progress,omitempty"`
	OriginTraceparent string               `json:"origin_traceparent,omitempty"`
}

type DeliveryProgress struct {
	TextSent         bool   `json:"text_sent,omitempty"`
	TextPartsSent    int    `json:"text_parts_sent,omitempty"`
	ImagesSent       int    `json:"images_sent,omitempty"`
	FilesSent        int    `json:"files_sent,omitempty"`
	MicrosoftDraftID string `json:"microsoft_draft_id,omitempty"`
}

type DeliveryFile struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	MIME          string `json:"mime,omitempty"`
	Size          int64  `json:"size,omitempty"`
	LocalPath     string `json:"local_path,omitempty"`
	LocalizeError string `json:"localize_error,omitempty"`
}
