package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	AuditSuccess = "success"
	AuditFailure = "failure"
	AuditDenied  = "denied"
)

// AuditLog is one append-only administrator operation record.
type AuditLog struct {
	ID           int64          `json:"id"`
	OccurredAt   time.Time      `json:"occurred_at"`
	RequestID    string         `json:"request_id"`
	Actor        string         `json:"actor"`
	SessionID    string         `json:"session_id"`
	RemoteIP     string         `json:"remote_ip"`
	UserAgent    string         `json:"user_agent"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Outcome      string         `json:"outcome"`
	HTTPMethod   string         `json:"http_method"`
	Route        string         `json:"route"`
	StatusCode   int            `json:"status_code"`
	ErrorCode    string         `json:"error_code,omitempty"`
	DurationMS   int64          `json:"duration_ms"`
	Details      map[string]any `json:"details"`
}

// AuditQuery filters administrator operation records.
type AuditQuery struct {
	Action       string
	Outcome      string
	ResourceType string
	Q            string
	From         time.Time
	To           time.Time
	Limit        int
	Offset       int
}

type auditLogRow struct {
	ID           int64  `gorm:"column:id;primaryKey;autoIncrement"`
	OccurredAt   int64  `gorm:"column:occurred_at;not null"`
	RequestID    string `gorm:"column:request_id;not null"`
	Actor        string `gorm:"column:actor;not null"`
	SessionID    string `gorm:"column:session_id;not null"`
	RemoteIP     string `gorm:"column:remote_ip;not null"`
	UserAgent    string `gorm:"column:user_agent;not null"`
	Action       string `gorm:"column:action;not null"`
	ResourceType string `gorm:"column:resource_type;not null"`
	ResourceID   string `gorm:"column:resource_id;not null"`
	Outcome      string `gorm:"column:outcome;not null"`
	HTTPMethod   string `gorm:"column:http_method;not null"`
	Route        string `gorm:"column:route;not null"`
	StatusCode   int    `gorm:"column:status_code;not null"`
	ErrorCode    string `gorm:"column:error_code;not null"`
	DurationMS   int64  `gorm:"column:duration_ms;not null"`
	DetailsJSON  string `gorm:"column:details_json;not null"`
}

func (auditLogRow) TableName() string { return "audit_logs" }

// AppendAudit appends one operation record.
func (s *Store) AppendAudit(entry AuditLog) (AuditLog, error) {
	if strings.TrimSpace(entry.RequestID) == "" || strings.TrimSpace(entry.Action) == "" {
		return AuditLog{}, errors.New("audit request id and action are required")
	}
	if entry.Outcome != AuditSuccess && entry.Outcome != AuditFailure && entry.Outcome != AuditDenied {
		return AuditLog{}, fmt.Errorf("invalid audit outcome %q", entry.Outcome)
	}
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now()
	}
	if entry.Details == nil {
		entry.Details = map[string]any{}
	}
	details, err := json.Marshal(entry.Details)
	if err != nil {
		return AuditLog{}, fmt.Errorf("encoding audit details: %w", err)
	}
	row := auditLogRow{
		OccurredAt: entry.OccurredAt.UnixMilli(), RequestID: entry.RequestID,
		Actor: entry.Actor, SessionID: entry.SessionID, RemoteIP: entry.RemoteIP, UserAgent: entry.UserAgent,
		Action: entry.Action, ResourceType: entry.ResourceType, ResourceID: entry.ResourceID,
		Outcome: entry.Outcome, HTTPMethod: entry.HTTPMethod, Route: entry.Route,
		StatusCode: entry.StatusCode, ErrorCode: entry.ErrorCode, DurationMS: entry.DurationMS,
		DetailsJSON: string(details),
	}
	if err := s.db.Create(&row).Error; err != nil {
		return AuditLog{}, fmt.Errorf("appending audit log: %w", err)
	}
	entry.ID = row.ID
	return entry, nil
}

// QueryAuditLogs returns matching operation records newest first.
func (s *Store) QueryAuditLogs(query AuditQuery) ([]AuditLog, int, error) {
	db := s.db.Model(&auditLogRow{})
	if query.Action != "" {
		db = db.Where("action = ?", query.Action)
	}
	if query.Outcome != "" {
		db = db.Where("outcome = ?", query.Outcome)
	}
	if query.ResourceType != "" {
		db = db.Where("resource_type = ?", query.ResourceType)
	}
	if !query.From.IsZero() {
		db = db.Where("occurred_at >= ?", query.From.UnixMilli())
	}
	if !query.To.IsZero() {
		db = db.Where("occurred_at < ?", query.To.UnixMilli())
	}
	if value := strings.TrimSpace(query.Q); value != "" {
		like := "%" + value + "%"
		db = db.Where("action LIKE ? OR resource_id LIKE ? OR remote_ip LIKE ? OR request_id LIKE ?", like, like, like, like)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("counting audit logs: %w", err)
	}
	limit, offset := normalizeAuditPage(query.Limit, query.Offset)
	var rows []auditLogRow
	if err := db.Order("occurred_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("querying audit logs: %w", err)
	}
	entries := make([]AuditLog, 0, len(rows))
	for _, row := range rows {
		entry, err := row.toAuditLog()
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, entry)
	}
	return entries, int(total), nil
}

// PruneAuditLogs removes at most limit records older than before.
func (s *Store) PruneAuditLogs(before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, errors.New("audit prune limit must be positive")
	}
	result := s.db.Exec(`DELETE FROM audit_logs WHERE id IN (
		SELECT id FROM audit_logs WHERE occurred_at < ? ORDER BY occurred_at ASC LIMIT ?
	)`, before.UnixMilli(), limit)
	if result.Error != nil {
		return 0, fmt.Errorf("pruning audit logs: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (row auditLogRow) toAuditLog() (AuditLog, error) {
	details := make(map[string]any)
	if err := json.Unmarshal([]byte(row.DetailsJSON), &details); err != nil {
		return AuditLog{}, fmt.Errorf("decoding audit log %d details: %w", row.ID, err)
	}
	return AuditLog{
		ID: row.ID, OccurredAt: time.UnixMilli(row.OccurredAt), RequestID: row.RequestID,
		Actor: row.Actor, SessionID: row.SessionID, RemoteIP: row.RemoteIP, UserAgent: row.UserAgent,
		Action: row.Action, ResourceType: row.ResourceType, ResourceID: row.ResourceID,
		Outcome: row.Outcome, HTTPMethod: row.HTTPMethod, Route: row.Route,
		StatusCode: row.StatusCode, ErrorCode: row.ErrorCode, DurationMS: row.DurationMS, Details: details,
	}, nil
}

func normalizeAuditPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	return limit, max(offset, 0)
}
