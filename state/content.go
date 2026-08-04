package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/linxin2429/bili_notify/model"
	_ "modernc.org/sqlite"
)

const (
	defaultContentLimit = 20
	maxContentLimit     = 100
)

// ContentQuery filters archived dynamics or comments.
// Time range is half-open [From, To). Zero times mean unbounded.
type ContentQuery struct {
	UID    string
	Q      string
	From   time.Time
	To     time.Time
	Limit  int
	Offset int
}

// DynamicRecord is a list-row projection of an archived dynamic.
type DynamicRecord struct {
	ID           string    `json:"id"`
	UID          string    `json:"uid"`
	UPName       string    `json:"up_name"`
	Type         string    `json:"type"`
	PublishedAt  time.Time `json:"published_at"`
	DiscoveredAt time.Time `json:"discovered_at"`
	Baseline     bool      `json:"baseline"`
	Title        string    `json:"title,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Description  string    `json:"description,omitempty"`
	URL          string    `json:"url,omitempty"`
	TargetURL    string    `json:"target_url,omitempty"`
	Badge        string    `json:"badge,omitempty"`
}

// CommentRecord is a list-row projection of an archived UP reply.
type CommentRecord struct {
	RPID         string    `json:"rpid"`
	UPUID        string    `json:"up_uid"`
	UPName       string    `json:"up_name"`
	ContentType  string    `json:"content_type,omitempty"`
	ContentID    string    `json:"content_id,omitempty"`
	ContentTitle string    `json:"content_title,omitempty"`
	ContentURL   string    `json:"content_url,omitempty"`
	PublishedAt  time.Time `json:"published_at"`
	DiscoveredAt time.Time `json:"discovered_at"`
	Baseline     bool      `json:"baseline"`
	Incomplete   bool      `json:"incomplete,omitempty"`
}

// ContentStore is the SQLite archive of collected dynamics and UP replies.
type ContentStore struct {
	db *sql.DB
}

// OpenContent opens (or creates) the content archive at path and migrates schema.
func OpenContent(path string) (*ContentStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening content database: %w", err)
	}
	// Single-writer; keep a small pool and enable WAL for concurrent readers.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configuring content database: %w", err)
	}
	cs := &ContentStore{db: db}
	if err := cs.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return cs, nil
}

func (c *ContentStore) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *ContentStore) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS dynamics (
  id            TEXT PRIMARY KEY,
  uid           TEXT NOT NULL,
  up_name       TEXT NOT NULL,
  type          TEXT NOT NULL,
  published_at  INTEGER NOT NULL,
  discovered_at INTEGER NOT NULL,
  baseline      INTEGER NOT NULL DEFAULT 0,
  title         TEXT NOT NULL DEFAULT '',
  summary       TEXT NOT NULL DEFAULT '',
  description   TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL DEFAULT '',
  target_url    TEXT NOT NULL DEFAULT '',
  badge         TEXT NOT NULL DEFAULT '',
  search_text   TEXT NOT NULL,
  payload_json  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dyn_pub ON dynamics(published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_dyn_uid_pub ON dynamics(uid, published_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS comments (
  rpid          TEXT PRIMARY KEY,
  up_uid        TEXT NOT NULL,
  up_name       TEXT NOT NULL,
  content_type  TEXT NOT NULL DEFAULT '',
  content_id    TEXT NOT NULL DEFAULT '',
  content_title TEXT NOT NULL DEFAULT '',
  content_url   TEXT NOT NULL DEFAULT '',
  published_at  INTEGER NOT NULL,
  discovered_at INTEGER NOT NULL,
  baseline      INTEGER NOT NULL DEFAULT 0,
  incomplete    INTEGER NOT NULL DEFAULT 0,
  search_text   TEXT NOT NULL,
  payload_json  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cmt_pub ON comments(published_at DESC, rpid DESC);
CREATE INDEX IF NOT EXISTS idx_cmt_uid_pub ON comments(up_uid, published_at DESC, rpid DESC);
`
	if _, err := c.db.Exec(schema); err != nil {
		return fmt.Errorf("migrating content schema: %w", err)
	}
	return nil
}

// ArchiveDynamics inserts unseen dynamics (INSERT OR IGNORE). Skips uid "system".
func (c *ContentStore) ArchiveDynamics(dynamics []model.Dynamic, baseline bool) error {
	if c == nil || len(dynamics) == 0 {
		return nil
	}
	now := time.Now().Unix()
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
INSERT OR IGNORE INTO dynamics
  (id, uid, up_name, type, published_at, discovered_at, baseline,
   title, summary, description, url, target_url, badge, search_text, payload_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, d := range dynamics {
		if d.ID == "" || d.UID == "system" {
			continue
		}
		payload, err := json.Marshal(d)
		if err != nil {
			return fmt.Errorf("encoding dynamic %s: %w", d.ID, err)
		}
		_, err = stmt.Exec(
			d.ID, d.UID, d.UPName, d.Type,
			d.PublishedAt.Unix(), now, boolToInt(baseline),
			d.Title, d.Summary, d.Description, d.URL, d.TargetURL, d.Badge,
			dynamicSearchText(d), string(payload),
		)
		if err != nil {
			return fmt.Errorf("archiving dynamic %s: %w", d.ID, err)
		}
	}
	return tx.Commit()
}

// ArchiveComments inserts unseen UP-reply notifications (INSERT OR IGNORE).
func (c *ContentStore) ArchiveComments(notes []model.CommentNotification, baseline bool) error {
	if c == nil || len(notes) == 0 {
		return nil
	}
	now := time.Now().Unix()
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
INSERT OR IGNORE INTO comments
  (rpid, up_uid, up_name, content_type, content_id, content_title, content_url,
   published_at, discovered_at, baseline, incomplete, search_text, payload_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, n := range notes {
		if n.RPID == "" {
			continue
		}
		payload, err := json.Marshal(n)
		if err != nil {
			return fmt.Errorf("encoding comment %s: %w", n.RPID, err)
		}
		_, err = stmt.Exec(
			n.RPID, n.UPUID, n.UPName, n.ContentType, n.ContentID, n.ContentTitle, n.ContentURL,
			n.PublishedAt.Unix(), now, boolToInt(baseline), boolToInt(n.Incomplete),
			commentSearchText(n), string(payload),
		)
		if err != nil {
			return fmt.Errorf("archiving comment %s: %w", n.RPID, err)
		}
	}
	return tx.Commit()
}

// DeleteUPContent removes all archived content for one UP.
func (c *ContentStore) DeleteUPContent(uid string) error {
	if c == nil || uid == "" {
		return nil
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM dynamics WHERE uid = ?`, uid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM comments WHERE up_uid = ?`, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// QueryDynamics returns matching archived dynamics and the total count for the filter.
func (c *ContentStore) QueryDynamics(q ContentQuery) ([]DynamicRecord, int, error) {
	if c == nil {
		return nil, 0, errors.New("content store is closed")
	}
	limit, offset := normalizePage(q.Limit, q.Offset)
	where, args := buildWhere(q, "uid")

	var total int
	countSQL := `SELECT COUNT(*) FROM dynamics` + where
	if err := c.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listSQL := `SELECT id, uid, up_name, type, published_at, discovered_at, baseline,
		title, summary, description, url, target_url, badge
		FROM dynamics` + where + ` ORDER BY published_at DESC, id DESC LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := c.db.Query(listSQL, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]DynamicRecord, 0, limit)
	for rows.Next() {
		var r DynamicRecord
		var pub, disc int64
		var base int
		if err := rows.Scan(
			&r.ID, &r.UID, &r.UPName, &r.Type, &pub, &disc, &base,
			&r.Title, &r.Summary, &r.Description, &r.URL, &r.TargetURL, &r.Badge,
		); err != nil {
			return nil, 0, err
		}
		r.PublishedAt = time.Unix(pub, 0)
		r.DiscoveredAt = time.Unix(disc, 0)
		r.Baseline = base != 0
		items = append(items, r)
	}
	return items, total, rows.Err()
}

// QueryComments returns matching archived comments and the total count for the filter.
func (c *ContentStore) QueryComments(q ContentQuery) ([]CommentRecord, int, error) {
	if c == nil {
		return nil, 0, errors.New("content store is closed")
	}
	limit, offset := normalizePage(q.Limit, q.Offset)
	where, args := buildWhere(q, "up_uid")

	var total int
	countSQL := `SELECT COUNT(*) FROM comments` + where
	if err := c.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listSQL := `SELECT rpid, up_uid, up_name, content_type, content_id, content_title, content_url,
		published_at, discovered_at, baseline, incomplete
		FROM comments` + where + ` ORDER BY published_at DESC, rpid DESC LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := c.db.Query(listSQL, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]CommentRecord, 0, limit)
	for rows.Next() {
		var r CommentRecord
		var pub, disc int64
		var base, incomplete int
		if err := rows.Scan(
			&r.RPID, &r.UPUID, &r.UPName, &r.ContentType, &r.ContentID, &r.ContentTitle, &r.ContentURL,
			&pub, &disc, &base, &incomplete,
		); err != nil {
			return nil, 0, err
		}
		r.PublishedAt = time.Unix(pub, 0)
		r.DiscoveredAt = time.Unix(disc, 0)
		r.Baseline = base != 0
		r.Incomplete = incomplete != 0
		items = append(items, r)
	}
	return items, total, rows.Err()
}

// GetDynamic returns the full archived dynamic payload by id.
func (c *ContentStore) GetDynamic(id string) (model.Dynamic, error) {
	var raw string
	err := c.db.QueryRow(`SELECT payload_json FROM dynamics WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Dynamic{}, ErrNotFound
	}
	if err != nil {
		return model.Dynamic{}, err
	}
	var d model.Dynamic
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return model.Dynamic{}, err
	}
	return d, nil
}

// GetComment returns the full archived comment notification by rpid.
func (c *ContentStore) GetComment(rpid string) (model.CommentNotification, error) {
	var raw string
	err := c.db.QueryRow(`SELECT payload_json FROM comments WHERE rpid = ?`, rpid).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CommentNotification{}, ErrNotFound
	}
	if err != nil {
		return model.CommentNotification{}, err
	}
	var n model.CommentNotification
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return model.CommentNotification{}, err
	}
	return n, nil
}

func buildWhere(q ContentQuery, uidColumn string) (string, []any) {
	var clauses []string
	var args []any
	if uid := strings.TrimSpace(q.UID); uid != "" {
		clauses = append(clauses, uidColumn+` = ?`)
		args = append(args, uid)
	}
	if keyword := foldSearch(q.Q); keyword != "" {
		clauses = append(clauses, `search_text LIKE ?`)
		args = append(args, "%"+keyword+"%")
	}
	if !q.From.IsZero() {
		clauses = append(clauses, `published_at >= ?`)
		args = append(args, q.From.Unix())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, `published_at < ?`)
		args = append(args, q.To.Unix())
	}
	if len(clauses) == 0 {
		return "", args
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

func normalizePage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultContentLimit
	}
	if limit > maxContentLimit {
		limit = maxContentLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func dynamicSearchText(d model.Dynamic) string {
	parts := []string{d.UPName, d.Title, d.Summary, d.Description, d.Badge}
	for _, link := range d.Links {
		parts = append(parts, link.Text)
	}
	if d.Original != nil {
		parts = append(parts, dynamicSearchText(*d.Original))
	}
	return foldSearch(strings.Join(parts, " "))
}

func commentSearchText(n model.CommentNotification) string {
	parts := []string{n.UPName, n.ContentTitle}
	for _, node := range n.Thread {
		parts = append(parts, node.Name, node.Message)
	}
	return foldSearch(strings.Join(parts, " "))
}

// foldSearch lowercases ASCII letters for case-insensitive Latin match; CJK is unchanged.
func foldSearch(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r <= unicode.MaxASCII {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
