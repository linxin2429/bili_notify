package state

import (
	"fmt"
	"path"
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

func archiveDynamicsTx(tx *gorm.DB, dynamics []model.Dynamic, baselineMode DynamicBaselineMode) error {
	if len(dynamics) == 0 {
		return nil
	}
	now := time.Now().Unix()
	for _, d := range dynamics {
		if d.ID == "" || d.UID == "system" {
			continue
		}
		source := sourceRow{ID: model.SourceID(model.PlatformBilibili, d.UID), Platform: string(model.PlatformBilibili),
			Type: string(model.SourceBilibiliUP), ExternalID: d.UID, Name: d.UPName, BaselineState: string(model.BaselinePending)}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name"}),
		}).Create(&source).Error; err != nil {
			return fmt.Errorf("ensuring Bilibili source %s: %w", d.UID, err)
		}
		content, attachments := bilibiliDynamicContent(d, baselineMode.includes(d), time.Unix(now, 0))
		if err := archiveContentTx(tx, content, attachments); err != nil {
			return fmt.Errorf("archiving unified dynamic %s: %w", d.ID, err)
		}
	}
	return nil
}

func bilibiliDynamicContent(dynamic model.Dynamic, baseline bool, syncedAt time.Time) (model.Content, []model.Attachment) {
	contentType := model.ContentDynamic
	if dynamic.BVID != "" {
		contentType = model.ContentVideo
	} else if strings.Contains(strings.ToUpper(dynamic.Type), "ARTICLE") {
		contentType = model.ContentArticle
	}
	text := dynamic.Description
	if text == "" {
		text = dynamic.Summary
	}
	stats := make(map[string]int64)
	if dynamic.Stats != nil {
		stats["forwards"] = dynamic.Stats.Forwards
		stats["comments"] = dynamic.Stats.Comments
		stats["likes"] = dynamic.Stats.Likes
	}
	upstreamType := dynamic.Type
	if upstreamType == "" {
		upstreamType = "unknown"
	}
	content := model.Content{
		ID: model.ContentID(model.PlatformBilibili, dynamic.ID), Platform: model.PlatformBilibili,
		SourceID: model.SourceID(model.PlatformBilibili, dynamic.UID), ExternalID: dynamic.ID,
		AuthorID: dynamic.UID, AuthorName: dynamic.UPName, UpstreamType: upstreamType, Type: contentType,
		Title: dynamic.Title, Text: text, URL: dynamic.URL, PublishedAt: dynamic.PublishedAt,
		FirstSeenAt: syncedAt, LastSyncedAt: syncedAt, Stats: stats, Baseline: baseline,
	}
	attachments := make([]model.Attachment, 0, len(dynamic.Media))
	for index, item := range dynamic.Media {
		externalID := "media-" + fmt.Sprint(index)
		attachmentType := model.AttachmentImage
		attachments = append(attachments, model.Attachment{
			ID: content.ID + ":attachment:" + externalID, ContentID: content.ID, ExternalID: externalID,
			Type: attachmentType, FileName: path.Base(item.LocalPath), MIME: item.ContentType,
			Size: item.Size, Width: item.Width, Height: item.Height, RemoteURL: item.URL, LocalPath: item.LocalPath,
		})
	}
	return content, attachments
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
