package bilibili

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDynamic(t *testing.T) {
	t.Parallel()
	for _, timestamp := range []string{`1700000000`, `"1700000000"`} {
		t.Run(timestamp, func(t *testing.T) {
			t.Parallel()
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
			require.NoError(t, err)
			assert.Equal(t, "12345", got.ID)
			assert.Equal(t, "42", got.UID)
			assert.Equal(t, "tester", name)
			assert.Equal(t, "new video", got.Summary)
			assert.Equal(t, int64(1700000000), got.PublishedAt.Unix())
			assert.Equal(t, "title", got.Title)
			assert.Equal(t, "description", got.Description)
			assert.Equal(t, "https://www.bilibili.com/video/BV1", got.TargetURL)
			require.Len(t, got.Media, 1)
			assert.Equal(t, "https://i0.hdslb.com/cover.jpg", got.Media[0].URL)
			require.NotNil(t, got.Video)
			assert.Equal(t, "03:21", got.Video.Duration)
			require.NotNil(t, got.Stats)
			assert.Equal(t, int64(2), got.Stats.Comments)
			require.Len(t, got.Links, 1)
			assert.Equal(t, "https://www.bilibili.com/v/topic/detail", got.Links[0].URL)
		})
	}
}

func TestParseDynamicContentTypes(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			got, _, err := parseDynamic("42", json.RawMessage(tt.raw))
			require.NoError(t, err)
			assert.Equal(t, tt.wantTitle, got.Title)
			assert.Len(t, got.Media, tt.wantMedia)
		})
	}
}

func TestParseForwardedDynamic(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{
		"id_str":"10","type":"DYNAMIC_TYPE_FORWARD",
		"modules":{"module_author":{"name":"forwarder","pub_ts":2},"module_dynamic":{"desc":{"text":"recommended"},"major":null}},
		"orig":{"id_str":"9","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"mid":"7","name":"author","pub_ts":1},"module_dynamic":{"desc":{"text":"original body"},"major":null}}}
	}`)
	got, _, err := parseDynamic("42", raw)
	require.NoError(t, err)
	require.NotNil(t, got.Original)
	assert.Equal(t, "7", got.Original.UID)
	assert.Equal(t, "author", got.Original.UPName)
	assert.Equal(t, "original body", got.Original.Summary)
}

func TestParseDynamicDoesNotTruncateBody(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("动态正文", 300)
	raw := json.RawMessage(`{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":` + mustJSON(t, body) + `},"major":null}}}`)
	got, _, err := parseDynamic("42", raw)
	require.NoError(t, err)
	assert.Equal(t, body, got.Summary)
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func TestFlexibleIntUnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		want    flexibleInt
		wantErr bool
	}{
		{name: "bare", raw: `10`, want: 10},
		{name: "quoted", raw: `"10"`, want: 10},
		{name: "zero", raw: `0`, want: 0},
		{name: "quoted zero", raw: `"0"`, want: 0},
		{name: "null", raw: `null`, want: 0},
		{name: "empty string", raw: `""`, want: 0},
		{name: "quoted spaces", raw: `" 42 "`, want: 42},
		{name: "non-numeric", raw: `"abc"`, wantErr: true},
		{name: "float", raw: `1.5`, wantErr: true},
		{name: "quoted float", raw: `"1.5"`, wantErr: true},
		{name: "bool", raw: `true`, wantErr: true},
		{name: "object", raw: `{}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got flexibleInt
			err := json.Unmarshal([]byte(tt.raw), &got)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseDynamicAcceptsStringMediaDimensions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		raw        string
		wantWidth  int
		wantHeight int
		wantMedia  int
	}{
		{
			name:       "draw quoted dimensions",
			raw:        `{"id_str":"2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"draw"},"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg","width":"10","height":"20"}]}}}}}`,
			wantWidth:  10,
			wantHeight: 20,
			wantMedia:  1,
		},
		{
			name:       "draw mixed dimensions",
			raw:        `{"id_str":"2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"draw"},"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg","width":10,"height":"20"}]}}}}}`,
			wantWidth:  10,
			wantHeight: 20,
			wantMedia:  1,
		},
		{
			name:       "draw missing dimensions",
			raw:        `{"id_str":"2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"draw"},"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg"}]}}}}}`,
			wantWidth:  0,
			wantHeight: 0,
			wantMedia:  1,
		},
		{
			name:       "draw null dimensions",
			raw:        `{"id_str":"2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"draw"},"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg","width":null,"height":null}]}}}}}`,
			wantWidth:  0,
			wantHeight: 0,
			wantMedia:  1,
		},
		{
			name:       "opus quoted dimensions",
			raw:        `{"id_str":"6","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"opus":{"title":"opus","summary":{"text":"desc"},"pics":[{"url":"https://i0.hdslb.com/o.jpg","width":"1080","height":"720"}],"jump_url":"https://www.bilibili.com/opus/6"}}}}}`,
			wantWidth:  1080,
			wantHeight: 720,
			wantMedia:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := parseDynamic("42", json.RawMessage(tt.raw))
			require.NoError(t, err)
			require.Len(t, got.Media, tt.wantMedia)
			assert.Equal(t, tt.wantWidth, got.Media[0].Width)
			assert.Equal(t, tt.wantHeight, got.Media[0].Height)
		})
	}
}

func TestParseDynamicRejectsInvalidPayloads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "invalid timestamp",
			raw:  `{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"pub_ts":"not-a-timestamp"}}}`,
		},
		{
			name: "unknown type",
			raw:  `{"id_str":"1","type":"NEW_TYPE","modules":{"module_author":{"pub_ts":1}}}`,
		},
		{
			name: "unexpected major",
			raw:  `{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"pub_ts":1},"module_dynamic":{"major":{"archive":{"title":"wrong"}}}}}`,
		},
		{
			name: "missing content card",
			raw:  `{"id_str":"1","type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"pub_ts":1},"module_dynamic":{"major":null}}}`,
		},
		{
			name: "non-numeric draw width",
			raw:  `{"id_str":"2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg","width":"abc","height":20}]}}}}}`,
		},
		{
			name: "float draw width",
			raw:  `{"id_str":"2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg","width":1.5,"height":20}]}}}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseDynamic("42", json.RawMessage(tt.raw))
			require.Error(t, err)
		})
	}
}

func FuzzParseDynamic(f *testing.F) {
	seeds := []string{
		`{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"word"},"major":null}}}`,
		`{"id_str":"2","type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"mid":42,"name":"tester","pub_ts":1700000000},"module_dynamic":{"major":{"archive":{"title":"t","jump_url":"//www.bilibili.com/video/BV1"}}}}}`,
		`{"id_str":"3","type":"NEW_TYPE","modules":{"module_author":{"pub_ts":1}}}`,
		`not-json`,
		`{}`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		// Fuzz must never panic; success or schema error are both acceptable.
		_, _, _ = parseDynamic("42", json.RawMessage(raw))
	})
}
