package state

import (
	"time"

	"github.com/linxin2429/bili_notify/model"
)

const (
	metaKeyAdminHash       = "admin_password_hash"
	metaKeyRuntimeSettings = "runtime_settings"
	authSessionID          = "session"
	tableChannels          = "channels"
	tableAuthSession       = "auth_session"
)

type metaRow struct {
	Key   string `gorm:"column:key;primaryKey"`
	Value string `gorm:"column:value;not null"`
}

func (metaRow) TableName() string { return "meta" }

type upRow struct {
	UID                    string `gorm:"column:uid;primaryKey"`
	Name                   string `gorm:"column:name;not null;default:''"`
	Enabled                int    `gorm:"column:enabled;not null;default:0"`
	BaselineReady          int    `gorm:"column:baseline_ready;not null;default:0"`
	ExclusiveBaselineReady int    `gorm:"column:exclusive_baseline_ready;not null;default:0"`
	LastPollAt             *int64 `gorm:"column:last_poll_at"`
	LastSuccessAt          *int64 `gorm:"column:last_success_at"`
	LastError              string `gorm:"column:last_error;not null;default:''"`
	ConsecutiveFail        int    `gorm:"column:consecutive_fail;not null;default:0"`
}

func (upRow) TableName() string { return "ups" }

func upFromModel(up model.UP) upRow {
	row := upRow{
		UID:                    up.UID,
		Name:                   up.Name,
		Enabled:                boolToInt(up.Enabled),
		BaselineReady:          boolToInt(up.BaselineReady),
		ExclusiveBaselineReady: boolToInt(up.ExclusiveBaselineReady),
		LastError:              up.LastError,
		ConsecutiveFail:        up.ConsecutiveFail,
	}
	if !up.LastPollAt.IsZero() {
		v := up.LastPollAt.Unix()
		row.LastPollAt = &v
	}
	if !up.LastSuccessAt.IsZero() {
		v := up.LastSuccessAt.Unix()
		row.LastSuccessAt = &v
	}
	return row
}

func (r upRow) toModel() model.UP {
	up := model.UP{
		UID:                    r.UID,
		Name:                   r.Name,
		Enabled:                r.Enabled != 0,
		BaselineReady:          r.BaselineReady != 0,
		ExclusiveBaselineReady: r.ExclusiveBaselineReady != 0,
		LastError:              r.LastError,
		ConsecutiveFail:        r.ConsecutiveFail,
		FollowState:            model.FollowUnknown,
		CollectionRoute:        model.CollectionRouteSpace,
	}
	if r.LastPollAt != nil {
		up.LastPollAt = time.Unix(*r.LastPollAt, 0)
	}
	if r.LastSuccessAt != nil {
		up.LastSuccessAt = time.Unix(*r.LastSuccessAt, 0)
	}
	return up
}

type channelRow struct {
	ID     string `gorm:"column:id;primaryKey"`
	Sealed []byte `gorm:"column:sealed;not null"`
}

func (channelRow) TableName() string { return tableChannels }

type authSessionRow struct {
	ID     string `gorm:"column:id;primaryKey"`
	Sealed []byte `gorm:"column:sealed;not null"`
}

func (authSessionRow) TableName() string { return tableAuthSession }

type biliFeedStateRow struct {
	AccountUID     string `gorm:"column:account_uid;primaryKey"`
	UpdateBaseline string `gorm:"column:update_baseline;not null;default:''"`
	Initialized    int    `gorm:"column:initialized;not null;default:0"`
	UpdatedAt      int64  `gorm:"column:updated_at;not null"`
}

func (biliFeedStateRow) TableName() string { return "bili_feed_state" }

type upFollowRelationRow struct {
	AccountUID      string `gorm:"column:account_uid;primaryKey"`
	UPUID           string `gorm:"column:up_uid;primaryKey"`
	FollowState     string `gorm:"column:follow_state;not null;default:'unknown'"`
	SpaceSynced     int    `gorm:"column:space_synced;not null;default:0"`
	CheckedAt       *int64 `gorm:"column:checked_at"`
	LastSpacePollAt *int64 `gorm:"column:last_space_poll_at"`
}

func (upFollowRelationRow) TableName() string { return "up_follow_relations" }

type seenDynamicRow struct {
	UID       string `gorm:"column:uid;primaryKey"`
	DynamicID string `gorm:"column:dynamic_id;primaryKey"`
	SeenAt    int64  `gorm:"column:seen_at;not null"`
}

func (seenDynamicRow) TableName() string { return "seen_dynamics" }

type seenCommentRow struct {
	UID    string `gorm:"column:uid;primaryKey"`
	RPID   string `gorm:"column:rpid;primaryKey"`
	SeenAt int64  `gorm:"column:seen_at;not null"`
}

func (seenCommentRow) TableName() string { return "seen_comments" }

type commentTargetRow struct {
	UID           string `gorm:"column:uid;primaryKey"`
	CommentType   int    `gorm:"column:comment_type;primaryKey"`
	CommentOID    string `gorm:"column:comment_oid;primaryKey"`
	UPName        string `gorm:"column:up_name;not null;default:''"`
	DynamicID     string `gorm:"column:dynamic_id;not null;default:''"`
	ContentType   string `gorm:"column:content_type;not null;default:''"`
	Title         string `gorm:"column:title;not null;default:''"`
	URL           string `gorm:"column:url;not null;default:''"`
	PublishedAt   int64  `gorm:"column:published_at;not null;default:0"`
	CommentCount  int64  `gorm:"column:comment_count;not null;default:0"`
	Closed        int    `gorm:"column:closed;not null;default:0"`
	BaselineReady int    `gorm:"column:baseline_ready;not null;default:0"`
	LastPollAt    *int64 `gorm:"column:last_poll_at"`
	LastError     string `gorm:"column:last_error;not null;default:''"`
}

func (commentTargetRow) TableName() string { return "comment_targets" }

func commentTargetFromModel(t model.CommentTarget) commentTargetRow {
	row := commentTargetRow{
		UID:           t.UID,
		CommentType:   t.CommentType,
		CommentOID:    t.CommentOID,
		UPName:        t.UPName,
		DynamicID:     t.DynamicID,
		ContentType:   t.ContentType,
		Title:         t.Title,
		URL:           t.URL,
		PublishedAt:   t.PublishedAt.Unix(),
		CommentCount:  t.CommentCount,
		Closed:        boolToInt(t.Closed),
		BaselineReady: boolToInt(t.BaselineReady),
		LastError:     t.LastError,
	}
	if !t.LastPollAt.IsZero() {
		v := t.LastPollAt.Unix()
		row.LastPollAt = &v
	}
	if t.PublishedAt.IsZero() {
		row.PublishedAt = 0
	}
	return row
}

func (r commentTargetRow) toModel() model.CommentTarget {
	t := model.CommentTarget{
		UID:           r.UID,
		UPName:        r.UPName,
		DynamicID:     r.DynamicID,
		ContentType:   r.ContentType,
		Title:         r.Title,
		URL:           r.URL,
		CommentType:   r.CommentType,
		CommentOID:    r.CommentOID,
		PublishedAt:   time.Unix(r.PublishedAt, 0),
		CommentCount:  r.CommentCount,
		Closed:        r.Closed != 0,
		BaselineReady: r.BaselineReady != 0,
		LastError:     r.LastError,
	}
	if r.LastPollAt != nil {
		t.LastPollAt = time.Unix(*r.LastPollAt, 0)
	}
	if r.PublishedAt == 0 {
		t.PublishedAt = time.Time{}
	}
	return t
}

type deliveryRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	Kind        string `gorm:"column:kind;not null"`
	ChannelID   string `gorm:"column:channel_id;not null"`
	State       string `gorm:"column:state;not null"`
	Attempts    int    `gorm:"column:attempts;not null;default:0"`
	NextAt      int64  `gorm:"column:next_at;not null"`
	LastError   string `gorm:"column:last_error;not null;default:''"`
	CreatedAt   int64  `gorm:"column:created_at;not null"`
	PayloadJSON string `gorm:"column:payload_json;not null"`
}

func (deliveryRow) TableName() string { return "deliveries" }

type dynamicRow struct {
	ID           string `gorm:"column:id;primaryKey"`
	UID          string `gorm:"column:uid;not null"`
	UPName       string `gorm:"column:up_name;not null"`
	Type         string `gorm:"column:type;not null"`
	PublishedAt  int64  `gorm:"column:published_at;not null"`
	DiscoveredAt int64  `gorm:"column:discovered_at;not null"`
	Baseline     int    `gorm:"column:baseline;not null;default:0"`
	Title        string `gorm:"column:title;not null;default:''"`
	Summary      string `gorm:"column:summary;not null;default:''"`
	Description  string `gorm:"column:description;not null;default:''"`
	URL          string `gorm:"column:url;not null;default:''"`
	TargetURL    string `gorm:"column:target_url;not null;default:''"`
	Badge        string `gorm:"column:badge;not null;default:''"`
	SearchText   string `gorm:"column:search_text;not null"`
	PayloadJSON  string `gorm:"column:payload_json;not null"`
}

func (dynamicRow) TableName() string { return "dynamics" }

type commentRow struct {
	RPID         string `gorm:"column:rpid;primaryKey"`
	UPUID        string `gorm:"column:up_uid;not null"`
	UPName       string `gorm:"column:up_name;not null"`
	ContentType  string `gorm:"column:content_type;not null;default:''"`
	ContentID    string `gorm:"column:content_id;not null;default:''"`
	ContentTitle string `gorm:"column:content_title;not null;default:''"`
	ContentURL   string `gorm:"column:content_url;not null;default:''"`
	PublishedAt  int64  `gorm:"column:published_at;not null"`
	DiscoveredAt int64  `gorm:"column:discovered_at;not null"`
	Baseline     int    `gorm:"column:baseline;not null;default:0"`
	Incomplete   int    `gorm:"column:incomplete;not null;default:0"`
	SearchText   string `gorm:"column:search_text;not null"`
	PayloadJSON  string `gorm:"column:payload_json;not null"`
}

func (commentRow) TableName() string { return "comments" }
