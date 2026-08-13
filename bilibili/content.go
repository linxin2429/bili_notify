package bilibili

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/model"
)

// ContentResult is the neutral boundary value emitted by the Bilibili adapter.
type ContentResult struct {
	Content     model.Content
	Attachments []model.Attachment
	Snapshot    model.ContentSnapshot
	AutomaticAI *model.AIContentSnapshot
}

func AdaptDynamic(dynamic model.Dynamic, baseline bool, syncedAt time.Time) ContentResult {
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
		stats["forwards"], stats["comments"], stats["likes"] = dynamic.Stats.Forwards, dynamic.Stats.Comments, dynamic.Stats.Likes
	}
	upstreamType := dynamic.Type
	if upstreamType == "" {
		upstreamType = "unknown"
	}
	content := model.Content{ID: model.ContentID(model.PlatformBilibili, dynamic.ID), Platform: model.PlatformBilibili,
		SourceID: model.SourceID(model.PlatformBilibili, dynamic.UID), ExternalID: dynamic.ID, AuthorID: dynamic.UID,
		AuthorName: dynamic.UPName, UpstreamType: upstreamType, Type: contentType, Title: dynamic.Title, Text: text,
		URL: dynamic.URL, PublishedAt: dynamic.PublishedAt, FirstSeenAt: syncedAt, LastSyncedAt: syncedAt, Stats: stats, Baseline: baseline}
	attachments := make([]model.Attachment, 0, len(dynamic.Media))
	for index, item := range dynamic.Media {
		externalID := "media-" + fmt.Sprint(index)
		attachments = append(attachments, model.Attachment{ID: content.ID + ":attachment:" + externalID, ContentID: content.ID,
			ExternalID: externalID, Type: model.AttachmentImage, FileName: path.Base(item.LocalPath), MIME: item.ContentType,
			Size: item.Size, Width: item.Width, Height: item.Height, RemoteURL: item.URL, LocalPath: item.LocalPath})
	}
	snapshot := contentSnapshot(dynamic, content)
	result := ContentResult{Content: content, Attachments: attachments, Snapshot: snapshot}
	if dynamic.Type == "DYNAMIC_TYPE_AV" && strings.TrimSpace(dynamic.BVID) != "" {
		result.AutomaticAI = &model.AIContentSnapshot{ContentID: content.ID, SourceID: content.SourceID, BVID: dynamic.BVID,
			Author: dynamic.UPName, Title: dynamic.Title, URL: firstValue(dynamic.TargetURL, dynamic.URL)}
	}
	return result
}

func contentSnapshot(dynamic model.Dynamic, content model.Content) model.ContentSnapshot {
	snapshot := model.ContentSnapshot{Platform: model.PlatformBilibili, SourceID: content.SourceID,
		SourceName: firstValue(dynamic.SourceName, dynamic.UPName), ContentID: content.ID, ExternalID: content.ExternalID,
		AuthorID: content.AuthorID, AuthorName: content.AuthorName, Type: content.Type, UpstreamType: content.UpstreamType,
		Title: dynamic.Title, Text: dynamic.Summary, Description: dynamic.Description, URL: dynamic.URL, TargetURL: dynamic.TargetURL,
		Badge: dynamic.Badge, PublishedAt: dynamic.PublishedAt, Stats: maps.Clone(content.Stats), Files: slices.Clone(dynamic.Files)}
	for _, link := range dynamic.Links {
		snapshot.Links = append(snapshot.Links, model.SnapshotLink{Text: link.Text, URL: link.URL})
	}
	for index, item := range dynamic.Media {
		snapshot.Media = append(snapshot.Media, model.SnapshotMedia{ID: fmt.Sprintf("%s:media:%d", content.ID, index), Type: model.AttachmentImage,
			Kind: string(item.Kind), URL: item.URL, LocalPath: item.LocalPath, MIME: item.ContentType, Size: item.Size, Width: item.Width, Height: item.Height})
	}
	if dynamic.Video != nil || dynamic.BVID != "" {
		snapshot.Video = &model.SnapshotVideoMeta{BVID: dynamic.BVID}
		if dynamic.Video != nil {
			snapshot.Video.Duration, snapshot.Video.Views, snapshot.Video.Danmaku = dynamic.Video.Duration, dynamic.Video.Views, dynamic.Video.Danmaku
		}
	}
	if dynamic.Original != nil {
		original := AdaptDynamic(*dynamic.Original, false, dynamic.Original.PublishedAt)
		snapshot.ForwardOf = &original.Snapshot
	}
	return snapshot
}

func firstValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
