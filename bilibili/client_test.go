package bilibili

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDynamic(t *testing.T) {
	for _, timestamp := range []string{`1700000000`, `"1700000000"`} {
		t.Run(timestamp, func(t *testing.T) {
			raw := json.RawMessage(`{
				"id_str":"12345",
				"type":"DYNAMIC_TYPE_AV",
				"modules":{
					"module_author":{"mid":42,"name":"tester","pub_ts":` + timestamp + `},
					"module_dynamic":{"desc":{"text":"new video","rich_text_nodes":[{"orig_text":"topic","jump_url":"//www.bilibili.com/v/topic/detail"}]},"major":{"archive":{"title":"title","desc":"description","cover":"//i0.hdslb.com/cover.jpg","jump_url":"//www.bilibili.com/video/BV1","duration_text":"03:21","stat":{"play":"1.2万","danmaku":"88"}}}},
					"module_stat":{"forward":{"count":1},"comment":{"count":2},"like":{"count":3}}
				}
			}`)
			got, name, err := parseDynamic("42", raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != "12345" || got.UID != "42" || name != "tester" || got.Summary != "new video" || got.PublishedAt.Unix() != 1700000000 {
				t.Fatalf("parseDynamic() = %#v, %q", got, name)
			}
			if got.Title != "title" || got.Description != "description" || got.TargetURL != "https://www.bilibili.com/video/BV1" {
				t.Fatalf("content card = %#v", got)
			}
			if len(got.Media) != 1 || got.Media[0].URL != "https://i0.hdslb.com/cover.jpg" || got.Video == nil || got.Video.Duration != "03:21" {
				t.Fatalf("media/video = %#v %#v", got.Media, got.Video)
			}
			if got.Stats == nil || got.Stats.Comments != 2 || len(got.Links) != 1 || got.Links[0].URL != "https://www.bilibili.com/v/topic/detail" {
				t.Fatalf("stats/links = %#v %#v", got.Stats, got.Links)
			}
		})
	}
}

func TestParseDynamicContentTypes(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantTitle string
		wantMedia int
	}{
		{
			name: "word",
			raw:  `{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"word"},"major":null}}}`,
		},
		{
			name:      "draw",
			raw:       `{"id_str":"2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"draw"},"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg","width":10,"height":20},{"src":"https://i0.hdslb.com/2.jpg"}]}}}}}`,
			wantMedia: 2,
		},
		{
			name:      "article",
			raw:       `{"id_str":"3","type":"DYNAMIC_TYPE_ARTICLE","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"article":{"title":"article","desc":"desc","covers":["https://i0.hdslb.com/a.jpg"],"jump_url":"https://www.bilibili.com/read/cv1"}}}}}`,
			wantTitle: "article", wantMedia: 1,
		},
		{
			name:      "pgc",
			raw:       `{"id_str":"4","type":"DYNAMIC_TYPE_PGC","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"pgc":{"title":"episode","cover":"https://i0.hdslb.com/p.jpg","jump_url":"https://www.bilibili.com/bangumi/play/ep1","badge":{"text":"会员"}}}}}}`,
			wantTitle: "episode", wantMedia: 1,
		},
		{
			name:      "common",
			raw:       `{"id_str":"5","type":"DYNAMIC_TYPE_COMMON_SQUARE","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"common":{"title":"common","desc":"desc","cover":"https://i0.hdslb.com/c.jpg","jump_url":"https://www.bilibili.com/blackboard/x"}}}}}`,
			wantTitle: "common", wantMedia: 1,
		},
		{
			name:      "opus",
			raw:       `{"id_str":"6","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"opus":{"title":"opus","summary":{"text":"desc"},"pics":[{"url":"https://i0.hdslb.com/o.jpg"}],"jump_url":"https://www.bilibili.com/opus/6"}}}}}`,
			wantTitle: "opus", wantMedia: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := parseDynamic("42", json.RawMessage(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != tt.wantTitle || len(got.Media) != tt.wantMedia {
				t.Fatalf("parseDynamic() = %#v", got)
			}
		})
	}
}

func TestParseForwardedDynamic(t *testing.T) {
	raw := json.RawMessage(`{
		"id_str":"10","type":"DYNAMIC_TYPE_FORWARD",
		"modules":{"module_author":{"name":"forwarder","pub_ts":2},"module_dynamic":{"desc":{"text":"recommended"},"major":null}},
		"orig":{"id_str":"9","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"mid":"7","name":"author","pub_ts":1},"module_dynamic":{"desc":{"text":"original body"},"major":null}}}
	}`)
	got, _, err := parseDynamic("42", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Original == nil || got.Original.UID != "7" || got.Original.UPName != "author" || got.Original.Summary != "original body" {
		t.Fatalf("original = %#v", got.Original)
	}
}

func TestParseDynamicDoesNotTruncateBody(t *testing.T) {
	body := strings.Repeat("动态正文", 300)
	raw := json.RawMessage(`{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":` + mustJSON(t, body) + `},"major":null}}}`)
	got, _, err := parseDynamic("42", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != body {
		t.Fatalf("summary length = %d, want %d", len([]rune(got.Summary)), len([]rune(body)))
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseDynamicRejectsInvalidTimestamp(t *testing.T) {
	raw := json.RawMessage(`{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"pub_ts":"not-a-timestamp"}}}`)
	if _, _, err := parseDynamic("42", raw); err == nil {
		t.Fatal("parseDynamic() accepted invalid timestamp")
	}
}

func TestParseDynamicRejectsUnknownType(t *testing.T) {
	raw := json.RawMessage(`{"id_str":"1","type":"NEW_TYPE","modules":{"module_author":{"pub_ts":1}}}`)
	if _, _, err := parseDynamic("42", raw); err == nil {
		t.Fatal("parseDynamic() accepted unknown type")
	}
}

func TestParseDynamicRejectsUnexpectedMajor(t *testing.T) {
	raw := json.RawMessage(`{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"pub_ts":1},"module_dynamic":{"major":{"archive":{"title":"wrong"}}}}}`)
	if _, _, err := parseDynamic("42", raw); err == nil {
		t.Fatal("parseDynamic() accepted a mismatched major")
	}
}

func TestParseDynamicRejectsMissingContentCard(t *testing.T) {
	raw := json.RawMessage(`{"id_str":"1","type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"pub_ts":1},"module_dynamic":{"major":null}}}`)
	if _, _, err := parseDynamic("42", raw); err == nil {
		t.Fatal("parseDynamic() accepted a video without an archive card")
	}
}
