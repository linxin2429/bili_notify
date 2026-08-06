package bilibili

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSessionReturnsAccountIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/x/web-interface/nav", r.URL.Path)
		_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"isLogin":true,"mid":123,"uname":" tester "}}`)
	}))
	t.Cleanup(server.Close)

	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
	account, err := client.ValidateSession(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.BiliAccount{UID: "123", Name: "tester"}, account)
}

func TestFetchRelationsClassifiesAttributes(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/x/relation/relations", r.URL.Path)
		assert.Equal(t, "1,2,3,4,5", r.URL.Query().Get("fids"))
		_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"1":{"attribute":2},"2":{"attribute":6},"3":{"attribute":0},"4":{"attribute":128}}}`)
	}))
	t.Cleanup(server.Close)

	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
	states, err := client.FetchRelations(t.Context(), []string{"1", "2", "3", "4", "5"})
	require.NoError(t, err)
	assert.Equal(t, model.Followed, states["1"])
	assert.Equal(t, model.Followed, states["2"])
	assert.Equal(t, model.FollowUnfollowed, states["3"])
	assert.Equal(t, model.FollowUnfollowed, states["4"])
	assert.Equal(t, model.FollowUnknown, states["5"])
}

func TestAggregateFeedEndpoints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(t *testing.T, client *Client)
	}{
		{
			name: "update check",
			call: func(t *testing.T, client *Client) {
				update, err := client.CheckFeedUpdate(t.Context(), "old")
				require.NoError(t, err)
				assert.Equal(t, 2, update.UpdateNum)
			},
		},
		{
			name: "feed page",
			call: func(t *testing.T, client *Client) {
				page, err := client.FetchAllPage(t.Context(), "old", "next")
				require.NoError(t, err)
				assert.Equal(t, "new", page.UpdateBaseline)
				assert.Equal(t, 2, page.UpdateNum)
				assert.Equal(t, "after", page.Offset)
				assert.True(t, page.HasMore)
				assert.Len(t, page.Items, 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "old", r.URL.Query().Get("update_baseline"))
				switch r.URL.Path {
				case "/x/polymer/web-dynamic/v1/feed/all/update":
					assert.Equal(t, "all", r.URL.Query().Get("type"))
					_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"update_num":2}}`)
				case "/x/polymer/web-dynamic/v1/feed/all":
					assert.Equal(t, "next", r.URL.Query().Get("offset"))
					assert.Equal(t, dynamicFeatures, r.URL.Query().Get("features"))
					_, _ = fmt.Fprint(w, `{"code":0,"message":"0","data":{"has_more":true,"offset":"after","update_baseline":"new","update_num":2,"items":[{"id_str":"1"}]}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			tt.call(t, New(server.Client(), "test", WithBaseURLs(server.URL, server.URL)))
		})
	}
}

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
		name          string
		raw           string
		wantType      string
		wantTitle     string
		wantMedia     int
		wantExclusive bool
	}{
		{
			name:     "word",
			raw:      `{"id_str":"1","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"word"},"major":null}}}`,
			wantType: "DYNAMIC_TYPE_WORD",
		},
		{
			name:      "draw",
			wantType:  "DYNAMIC_TYPE_DRAW",
			raw:       `{"id_str":"2","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"draw"},"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg","width":10,"height":20},{"src":"https://i0.hdslb.com/2.jpg"}]}}}}}`,
			wantMedia: 2,
		},
		{
			name:      "article",
			wantType:  "DYNAMIC_TYPE_ARTICLE",
			raw:       `{"id_str":"3","type":"DYNAMIC_TYPE_ARTICLE","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"article":{"title":"article","desc":"desc","covers":["https://i0.hdslb.com/a.jpg"],"jump_url":"https://www.bilibili.com/read/cv1"}}}}}`,
			wantTitle: "article", wantMedia: 1,
		},
		{
			name:      "pgc",
			wantType:  "DYNAMIC_TYPE_PGC",
			raw:       `{"id_str":"4","type":"DYNAMIC_TYPE_PGC","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"pgc":{"title":"episode","cover":"https://i0.hdslb.com/p.jpg","jump_url":"https://www.bilibili.com/bangumi/play/ep1","badge":{"text":"会员"}}}}}}`,
			wantTitle: "episode", wantMedia: 1,
		},
		{
			name:      "common",
			wantType:  "DYNAMIC_TYPE_COMMON_SQUARE",
			raw:       `{"id_str":"5","type":"DYNAMIC_TYPE_COMMON_SQUARE","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"common":{"title":"common","desc":"desc","cover":"https://i0.hdslb.com/c.jpg","jump_url":"https://www.bilibili.com/blackboard/x"}}}}}`,
			wantTitle: "common", wantMedia: 1,
		},
		{
			name:      "opus",
			wantType:  "DYNAMIC_TYPE_DRAW",
			raw:       `{"id_str":"6","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"opus":{"title":"opus","summary":{"text":"desc"},"pics":[{"url":"https://i0.hdslb.com/o.jpg"}],"jump_url":"https://www.bilibili.com/opus/6"}}}}}`,
			wantTitle: "opus", wantMedia: 1,
		},
		{
			name:     "text-only draw opus is normalized",
			raw:      `{"id_str":"7","type":"DYNAMIC_TYPE_DRAW","basic":{"comment_type":11,"comment_id_str":"404357189"},"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"opus":{"summary":{"text":"text only"},"pics":[],"jump_url":"https://www.bilibili.com/opus/7"}}}}}`,
			wantType: "DYNAMIC_TYPE_WORD",
		},
		{
			name:     "word opus without pictures",
			raw:      `{"id_str":"8","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"opus":{"summary":{"text":"text only"},"pics":[],"jump_url":"https://www.bilibili.com/opus/8"}}}}}`,
			wantType: "DYNAMIC_TYPE_WORD",
		},
		{
			name:          "unlocked exclusive draw",
			raw:           `{"id_str":"9","type":"DYNAMIC_TYPE_DRAW","basic":{"is_only_fans":true,"comment_type":11,"comment_id_str":"9"},"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/9.jpg"}]}}}}}`,
			wantType:      "DYNAMIC_TYPE_DRAW",
			wantMedia:     1,
			wantExclusive: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := parseDynamic("42", json.RawMessage(tt.raw))
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, got.Type)
			assert.Equal(t, tt.wantTitle, got.Title)
			assert.Len(t, got.Media, tt.wantMedia)
			assert.Equal(t, tt.wantExclusive, got.Exclusive)
		})
	}
}

func TestFetchPageSendsAuthenticatedDynamicFeatures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		offset string
	}{
		{name: "first page"},
		{name: "next page", offset: "next-offset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			type observedRequest struct {
				path     string
				hostMID  string
				platform string
				features string
				offset   string
				session  string
			}
			requests := make(chan observedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cookie, _ := r.Cookie("SESSDATA")
				session := ""
				if cookie != nil {
					session = cookie.Value
				}
				requests <- observedRequest{
					path: r.URL.Path, hostMID: r.URL.Query().Get("host_mid"),
					platform: r.URL.Query().Get("platform"), features: r.URL.Query().Get("features"),
					offset: r.URL.Query().Get("offset"), session: session,
				}
				_, _ = io.WriteString(w, `{"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[]}}`)
			}))
			t.Cleanup(server.Close)

			client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
			client.SetSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "session-value"}})
			_, err := client.FetchPage(t.Context(), "42", tt.offset)
			require.NoError(t, err)
			got := <-requests
			assert.Equal(t, "/x/polymer/web-dynamic/v1/feed/space", got.path)
			assert.Equal(t, "42", got.hostMID)
			assert.Equal(t, "web", got.platform)
			assert.Equal(t, dynamicFeatures, got.features)
			assert.Equal(t, tt.offset, got.offset)
			assert.Equal(t, "session-value", got.session)
		})
	}
}

func TestFetchPageSkipsBlockedExclusiveDynamic(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"code":0,"message":"0","data":{"has_more":false,"offset":"","items":[
				{"id_str":"blocked","type":"DYNAMIC_TYPE_DRAW","basic":{"is_only_fans":true},"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"type":"MAJOR_TYPE_BLOCKED","blocked":{"hint_message":"充电后观看"}}}}},
				{"id_str":"normal","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":2},"module_dynamic":{"desc":{"text":"visible"},"major":null}}}
			]}
		}`)
	}))
	t.Cleanup(server.Close)

	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
	page, err := client.FetchPage(t.Context(), "42", "")
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "normal", page.Items[0].ID)
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
		{
			name: "word opus with pictures",
			raw:  `{"id_str":"3","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"opus":{"pics":[{"url":"https://i0.hdslb.com/3.jpg"}]}}}}}`,
		},
		{
			name: "blocked non-exclusive card",
			raw:  `{"id_str":"4","type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"type":"MAJOR_TYPE_BLOCKED","blocked":{"hint_message":"blocked"}}}}}`,
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

func TestCommentCoordinates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		raw             string
		wantable        bool
		wantType        int
		wantOID         string
		wantDynamicType string
	}{
		{
			name: "basic preferred over aid",
			raw: `{
				"id_str":"100","type":"DYNAMIC_TYPE_AV",
				"basic":{"comment_type":1,"comment_id_str":"999"},
				"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"archive":{"aid":111,"title":"t","jump_url":"//www.bilibili.com/video/BV1"}}}}
			}`,
			wantable: true, wantType: 1, wantOID: "999",
		},
		{
			name: "av falls back to aid",
			raw: `{
				"id_str":"101","type":"DYNAMIC_TYPE_AV",
				"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"archive":{"aid":"222","title":"t","jump_url":"//www.bilibili.com/video/BV1"}}}}
			}`,
			wantable: true, wantType: 1, wantOID: "222",
		},
		{
			name:     "word uses dynamic id",
			raw:      `{"id_str":"333","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"hi"},"major":null}}}`,
			wantable: true, wantType: 17, wantOID: "333",
		},
		{
			name: "forward uses forward id",
			raw: `{
				"id_str":"10","type":"DYNAMIC_TYPE_FORWARD",
				"modules":{"module_author":{"name":"forwarder","pub_ts":2},"module_dynamic":{"desc":{"text":"x"},"major":null}},
				"orig":{"id_str":"9","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"mid":"7","name":"author","pub_ts":1},"module_dynamic":{"desc":{"text":"o"},"major":null}}}
			}`,
			wantable: true, wantType: 17, wantOID: "10",
		},
		{
			name: "article from jump url",
			raw: `{
				"id_str":"3","type":"DYNAMIC_TYPE_ARTICLE",
				"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"article":{"title":"article","jump_url":"https://www.bilibili.com/read/cv98765"}}}}
			}`,
			wantable: true, wantType: 12, wantOID: "98765",
		},
		{
			name: "draw without basic is not commentable",
			raw: `{
				"id_str":"2","type":"DYNAMIC_TYPE_DRAW",
				"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"draw"},"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg"}]}}}}
			}`,
			wantable: false,
		},
		{
			name: "draw with basic album id",
			raw: `{
				"id_str":"2","type":"DYNAMIC_TYPE_DRAW",
				"basic":{"comment_type":11,"comment_id_str":"349795473"},
				"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"desc":{"text":"draw"},"major":{"draw":{"items":[{"src":"https://i0.hdslb.com/1.jpg"}]}}}}
			}`,
			wantable: true, wantType: 11, wantOID: "349795473",
		},
		{
			name: "text-only draw keeps album coordinates",
			raw: `{
				"id_str":"1232961308306964534","type":"DYNAMIC_TYPE_DRAW",
				"basic":{"comment_type":11,"comment_id_str":"404357189"},
				"modules":{"module_author":{"name":"up","pub_ts":1},"module_dynamic":{"major":{"opus":{"summary":{"text":"text only"},"pics":[]}}}}
			}`,
			wantable: true, wantType: 11, wantOID: "404357189", wantDynamicType: "DYNAMIC_TYPE_WORD",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := parseDynamic("42", json.RawMessage(tt.raw))
			require.NoError(t, err)
			assert.Equal(t, tt.wantable, got.Commentable)
			if tt.wantDynamicType != "" {
				assert.Equal(t, tt.wantDynamicType, got.Type)
			}
			if tt.wantable {
				assert.Equal(t, tt.wantType, got.CommentType)
				assert.Equal(t, tt.wantOID, got.CommentOID)
			}
		})
	}
}

func TestParseReplyList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantErr string
		check   func(t *testing.T, page ReplyPage)
	}{
		{
			name: "roots",
			body: `{
				"code":0,"message":"0","ttl":1,
				"data":{"page":{"num":1,"size":20,"count":2,"acount":5},"replies":[
					{"rpid_str":"11","root_str":"0","parent_str":"0","mid":1,"ctime":1700000000,"rcount":1,
					 "member":{"mid":"1","uname":"alice"},"content":{"message":"root"}},
					{"rpid":22,"root":0,"parent":0,"mid":2,"ctime":1700000001,"rcount":0,
					 "member":{"mid":"2","uname":"bob"},"content":{"message":"another"}}
				]}
			}`,
			check: func(t *testing.T, page ReplyPage) {
				require.Len(t, page.Replies, 2)
				assert.Equal(t, "11", page.Replies[0].RPID)
				assert.Equal(t, "alice", page.Replies[0].Name)
				assert.Equal(t, "root", page.Replies[0].Message)
				assert.Equal(t, "22", page.Replies[1].RPID)
				assert.Equal(t, int64(2), page.RootCount)
			},
		},
		{
			name:    "closed",
			body:    `{"code":12002,"message":"Comment area is closed","ttl":1,"data":null}`,
			wantErr: "12002",
		},
		{
			name:    "bad type",
			body:    `{"code":12009,"message":"invalid type","ttl":1,"data":null}`,
			wantErr: "schema",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			page, err := parseReplyList([]byte(tt.body), 20)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.wantErr))
				if tt.name == "closed" {
					assert.True(t, IsCommentClosed(err))
				}
				return
			}
			require.NoError(t, err)
			tt.check(t, page)
		})
	}
}
