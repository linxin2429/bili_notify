-- +goose Up
CREATE TABLE audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at INTEGER NOT NULL,
    request_id TEXT NOT NULL,
    actor TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    remote_ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL,
    http_method TEXT NOT NULL,
    route TEXT NOT NULL,
    status_code INTEGER NOT NULL,
    error_code TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL,
    details_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_audit_logs_occurred_at ON audit_logs (occurred_at DESC);
CREATE INDEX idx_audit_logs_action_time ON audit_logs (action, occurred_at DESC);
CREATE INDEX idx_audit_logs_outcome_time ON audit_logs (outcome, occurred_at DESC);
CREATE INDEX idx_audit_logs_resource_time ON audit_logs (resource_type, resource_id, occurred_at DESC);

-- +goose Down
DROP TABLE audit_logs;
