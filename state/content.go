package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/linxin2429/bili_notify/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	ID           string               `json:"id"`
	UID          string               `json:"uid"`
	UPName       string               `json:"up_name"`
	Type         string               `json:"type"`
	PublishedAt  time.Time            `json:"published_at"`
	DiscoveredAt time.Time            `json:"discovered_at"`
	Baseline     bool                 `json:"baseline"`
	Title        string               `json:"title,omitempty"`
	Summary      string               `json:"summary,omitempty"`
	Description  string               `json:"description,omitempty"`
	URL          string               `json:"url,omitempty"`
	TargetURL    string               `json:"target_url,omitempty"`
	Badge        string               `json:"badge,omitempty"`
	Media        []model.DynamicMedia `json:"media,omitempty"`
	Stats        *model.DynamicStats  `json:"stats,omitempty"`
	Video        *model.DynamicVideo  `json:"video,omitempty"`
	Original     *DynamicPreview      `json:"original,omitempty"`
}

// DynamicPreview contains the archived fields needed to identify a referenced dynamic.
type DynamicPreview struct {
	ID          string               `json:"id,omitempty"`
	UID         string               `json:"uid,omitempty"`
	UPName      string               `json:"up_name,omitempty"`
	Type        string               `json:"type,omitempty"`
	Title       string               `json:"title,omitempty"`
	Summary     string               `json:"summary,omitempty"`
	Description string               `json:"description,omitempty"`
	URL         string               `json:"url,omitempty"`
	TargetURL   string               `json:"target_url,omitempty"`
	Badge       string               `json:"badge,omitempty"`
	Media       []model.DynamicMedia `json:"media,omitempty"`
	Video       *model.DynamicVideo  `json:"video,omitempty"`
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

func archiveDynamicsTx(tx *gorm.DB, dynamics []model.Dynamic, baselineMode DynamicBaselineMode) error {
	if len(dynamics) == 0 {
		return nil
	}
	now := time.Now().Unix()
	for _, d := range dynamics {
		if d.ID == "" || d.UID == "system" {
			continue
		}
		payload, err := json.Marshal(d)
		if err != nil {
			return fmt.Errorf("encoding dynamic %s: %w", d.ID, err)
		}
		row := dynamicRow{
			ID:           d.ID,
			UID:          d.UID,
			UPName:       d.UPName,
			Type:         d.Type,
			PublishedAt:  d.PublishedAt.Unix(),
			DiscoveredAt: now,
			Baseline:     boolToInt(baselineMode.includes(d)),
			Title:        d.Title,
			Summary:      d.Summary,
			Description:  d.Description,
			URL:          d.URL,
			TargetURL:    d.TargetURL,
			Badge:        d.Badge,
			SearchText:   dynamicSearchText(d),
			PayloadJSON:  string(payload),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("archiving dynamic %s: %w", d.ID, err)
		}
	}
	return nil
}

func archiveCommentsTx(tx *gorm.DB, notes []model.CommentNotification, baseline bool) error {
	if len(notes) == 0 {
		return nil
	}
	now := time.Now().Unix()
	for _, n := range notes {
		if n.RPID == "" {
			continue
		}
		payload, err := json.Marshal(n)
		if err != nil {
			return fmt.Errorf("encoding comment %s: %w", n.RPID, err)
		}
		row := commentRow{
			RPID:         n.RPID,
			UPUID:        n.UPUID,
			UPName:       n.UPName,
			ContentType:  n.ContentType,
			ContentID:    n.ContentID,
			ContentTitle: n.ContentTitle,
			ContentURL:   n.ContentURL,
			PublishedAt:  n.PublishedAt.Unix(),
			DiscoveredAt: now,
			Baseline:     boolToInt(baseline),
			Incomplete:   boolToInt(n.Incomplete),
			SearchText:   commentSearchText(n),
			PayloadJSON:  string(payload),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("archiving comment %s: %w", n.RPID, err)
		}
	}
	return nil
}

// QueryDynamics returns matching archived dynamics and the total count for the filter.
func (s *Store) QueryDynamics(q ContentQuery) ([]DynamicRecord, int, error) {
	limit, offset := normalizePage(q.Limit, q.Offset)
	where, args := buildWhere(q, "uid")

	var total int64
	countSQL := `SELECT COUNT(*) FROM dynamics` + where
	if err := s.db.Raw(countSQL, args...).Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	listSQL := `SELECT id, uid, up_name, type, published_at, discovered_at, baseline,
		title, summary, description, url, target_url, badge, payload_json
		FROM dynamics` + where + ` ORDER BY published_at DESC, id DESC LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), limit, offset)
	var rows []dynamicRow
	if err := s.db.Raw(listSQL, listArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]DynamicRecord, 0, len(rows))
	for _, r := range rows {
		// List projection prefers denormalized text columns. Media/original come from
		// payload_json when present; a corrupt archive must not blank the whole page.
		record := DynamicRecord{
			ID:           r.ID,
			UID:          r.UID,
			UPName:       r.UPName,
			Type:         r.Type,
			PublishedAt:  time.Unix(r.PublishedAt, 0),
			DiscoveredAt: time.Unix(r.DiscoveredAt, 0),
			Baseline:     r.Baseline != 0,
			Title:        r.Title,
			Summary:      r.Summary,
			Description:  r.Description,
			URL:          r.URL,
			TargetURL:    r.TargetURL,
			Badge:        r.Badge,
		}
		if strings.TrimSpace(r.PayloadJSON) != "" {
			var payload model.Dynamic
			if err := json.Unmarshal([]byte(r.PayloadJSON), &payload); err == nil {
				record.Media = payload.Media
				record.Stats = payload.Stats
				record.Video = payload.Video
				record.Original = dynamicPreview(payload.Original)
			}
		}
		items = append(items, record)
	}
	return items, int(total), nil
}

func dynamicPreview(dynamic *model.Dynamic) *DynamicPreview {
	if dynamic == nil {
		return nil
	}
	return &DynamicPreview{
		ID: dynamic.ID, UID: dynamic.UID, UPName: dynamic.UPName, Type: dynamic.Type,
		Title: dynamic.Title, Summary: dynamic.Summary, Description: dynamic.Description,
		URL: dynamic.URL, TargetURL: dynamic.TargetURL, Badge: dynamic.Badge, Media: dynamic.Media,
		Video: dynamic.Video,
	}
}

// QueryComments returns matching archived comments and the total count for the filter.
func (s *Store) QueryComments(q ContentQuery) ([]CommentRecord, int, error) {
	limit, offset := normalizePage(q.Limit, q.Offset)
	where, args := buildWhere(q, "up_uid")

	var total int64
	countSQL := `SELECT COUNT(*) FROM comments` + where
	if err := s.db.Raw(countSQL, args...).Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	listSQL := `SELECT rpid, up_uid, up_name, content_type, content_id, content_title, content_url,
		published_at, discovered_at, baseline, incomplete
		FROM comments` + where + ` ORDER BY published_at DESC, rpid DESC LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), limit, offset)
	var rows []commentRow
	if err := s.db.Raw(listSQL, listArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]CommentRecord, 0, len(rows))
	for _, r := range rows {
		items = append(items, CommentRecord{
			RPID:         r.RPID,
			UPUID:        r.UPUID,
			UPName:       r.UPName,
			ContentType:  r.ContentType,
			ContentID:    r.ContentID,
			ContentTitle: r.ContentTitle,
			ContentURL:   r.ContentURL,
			PublishedAt:  time.Unix(r.PublishedAt, 0),
			DiscoveredAt: time.Unix(r.DiscoveredAt, 0),
			Baseline:     r.Baseline != 0,
			Incomplete:   r.Incomplete != 0,
		})
	}
	return items, int(total), nil
}

// GetDynamic returns the full archived dynamic payload by id.
func (s *Store) GetDynamic(id string) (model.Dynamic, error) {
	var row dynamicRow
	err := s.db.Select("payload_json").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Dynamic{}, ErrNotFound
	}
	if err != nil {
		return model.Dynamic{}, err
	}
	var d model.Dynamic
	if err := json.Unmarshal([]byte(row.PayloadJSON), &d); err != nil {
		return model.Dynamic{}, err
	}
	return d, nil
}

// GetComment returns the full archived comment notification by rpid.
func (s *Store) GetComment(rpid string) (model.CommentNotification, error) {
	var row commentRow
	err := s.db.Select("payload_json").Where("rpid = ?", rpid).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CommentNotification{}, ErrNotFound
	}
	if err != nil {
		return model.CommentNotification{}, err
	}
	var n model.CommentNotification
	if err := json.Unmarshal([]byte(row.PayloadJSON), &n); err != nil {
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
