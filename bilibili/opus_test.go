package bilibili

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeedsArticleEnrichment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dynamic model.Dynamic
		want    bool
	}{
		{name: "article", dynamic: model.Dynamic{Type: "DYNAMIC_TYPE_ARTICLE"}, want: true},
		{name: "word", dynamic: model.Dynamic{Type: "DYNAMIC_TYPE_WORD"}},
		{name: "forwarded article", dynamic: model.Dynamic{Type: "DYNAMIC_TYPE_FORWARD", Original: &model.Dynamic{Type: "DYNAMIC_TYPE_ARTICLE"}}, want: true},
		{name: "forwarded word", dynamic: model.Dynamic{Type: "DYNAMIC_TYPE_FORWARD", Original: &model.Dynamic{Type: "DYNAMIC_TYPE_WORD"}}},
		{name: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NeedsArticleEnrichment(tt.dynamic))
		})
	}
}

func TestFetchOpusDetailSendsAuthenticatedFeatures(t *testing.T) {
	t.Parallel()
	type observed struct {
		path     string
		id       string
		features string
		session  string
	}
	requests := make(chan observed, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("SESSDATA")
		session := ""
		if cookie != nil {
			session = cookie.Value
		}
		requests <- observed{path: r.URL.Path, id: r.URL.Query().Get("id"), features: r.URL.Query().Get("features"), session: session}
		_, _ = io.WriteString(w, opusDetailEnvelope(opusDetailItem("9", "标题", []string{
			opusTextParagraph("完整正文"),
			opusPicParagraph("https://i0.hdslb.com/body.jpg", 100, 50),
		})))
	}))
	t.Cleanup(server.Close)

	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
	client.SetSession(model.BiliSession{Cookies: map[string]string{"SESSDATA": "session-value"}})
	got, err := client.fetchOpusDetail(t.Context(), "9")
	require.NoError(t, err)
	assert.Equal(t, "标题", got.Title)
	assert.Equal(t, "完整正文", got.Body)
	require.Len(t, got.Media, 1)
	assert.Equal(t, "https://i0.hdslb.com/body.jpg", got.Media[0].URL)
	assert.Equal(t, 100, got.Media[0].Width)
	assert.Equal(t, 50, got.Media[0].Height)
	observedReq := <-requests
	assert.Equal(t, "/x/polymer/web-dynamic/v1/opus/detail", observedReq.path)
	assert.Equal(t, "9", observedReq.id)
	assert.Equal(t, opusDetailFeatures, observedReq.features)
	assert.Equal(t, "session-value", observedReq.session)
}

func TestEnrichArticleReplacesTruncatedCard(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/x/polymer/web-dynamic/v1/opus/detail", r.URL.Path)
		_, _ = io.WriteString(w, opusDetailEnvelope(opusDetailItem("3", "完整标题", []string{
			opusTextParagraph("第一段"),
			opusPicParagraph("https://i0.hdslb.com/1.jpg", 10, 20),
			opusTextParagraph("第二段"),
			opusPicParagraph("https://i0.hdslb.com/2.jpg", 0, 0),
		})))
	}))
	t.Cleanup(server.Close)

	dynamic := model.Dynamic{
		ID: "3", Type: "DYNAMIC_TYPE_ARTICLE", Title: "卡片标题",
		Summary: "第一段...", Description: "第一段...",
		Media: []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: "https://i0.hdslb.com/cover.jpg"}},
		Links: []model.DynamicLink{{Text: "专栏", URL: "https://www.bilibili.com/read/cv1"}},
	}
	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
	require.NoError(t, client.EnrichArticle(t.Context(), &dynamic))
	assert.Equal(t, "完整标题", dynamic.Title)
	assert.Empty(t, dynamic.Summary)
	assert.Equal(t, "第一段\n\n第二段", dynamic.Description)
	require.Len(t, dynamic.Media, 2)
	assert.Equal(t, model.DynamicMediaImage, dynamic.Media[0].Kind)
	assert.Equal(t, "https://i0.hdslb.com/1.jpg", dynamic.Media[0].URL)
	assert.Equal(t, "https://i0.hdslb.com/2.jpg", dynamic.Media[1].URL)
	assert.Equal(t, "https://www.bilibili.com/read/cv1", dynamic.Links[0].URL)
}

func TestEnrichArticleWalksForwardedOriginal(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "9", r.URL.Query().Get("id"))
		_, _ = io.WriteString(w, opusDetailEnvelope(opusDetailItem("9", "原文标题", []string{opusTextParagraph("完整原文"), opusPicParagraph("https://i0.hdslb.com/orig.jpg", 1, 1)})))
	}))
	t.Cleanup(server.Close)

	dynamic := model.Dynamic{
		ID: "10", Type: "DYNAMIC_TYPE_FORWARD", Summary: "推荐",
		Original: &model.Dynamic{ID: "9", Type: "DYNAMIC_TYPE_ARTICLE", Description: "截断"},
	}
	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
	require.NoError(t, client.EnrichArticle(t.Context(), &dynamic))
	assert.Equal(t, "推荐", dynamic.Summary)
	require.NotNil(t, dynamic.Original)
	assert.Equal(t, "原文标题", dynamic.Original.Title)
	assert.Equal(t, "完整原文", dynamic.Original.Description)
	require.Len(t, dynamic.Original.Media, 1)
}

func TestEnrichArticleSkipsNonArticle(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("non-article dynamics must not fetch opus detail")
	}))
	t.Cleanup(server.Close)

	dynamic := model.Dynamic{ID: "1", Type: "DYNAMIC_TYPE_WORD", Summary: "hello"}
	client := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL))
	require.NoError(t, client.EnrichArticle(t.Context(), &dynamic))
	assert.Equal(t, "hello", dynamic.Summary)
}

func TestParseOpusDetailParagraphs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		paragraph string
		wantBody  string
		wantMedia int
		wantLink  string
	}{
		{name: "text", paragraph: opusTextParagraph("hello"), wantBody: "hello"},
		{name: "html entities", paragraph: opusTextParagraph("A &amp; B"), wantBody: "A & B"},
		{name: "quote", paragraph: `{"para_type":4,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":"line1\nline2"}}]}}`, wantBody: "> line1\n> line2"},
		{name: "unordered list", paragraph: `{"para_type":5,"list":{"style":2,"items":[{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":"a"}}]},{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":"b"}}]}]}}`, wantBody: "- a\n- b"},
		{name: "ordered list", paragraph: `{"para_type":5,"list":{"style":1,"items":[{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":"a"}}]},{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":"b"}}]}]}}`, wantBody: "1. a\n2. b"},
		{name: "code", paragraph: `{"para_type":7,"code":{"content":"{&quot;ok&quot;:true}","lang":"language-json"}}`, wantBody: `{"ok":true}`},
		{name: "picture", paragraph: opusPicParagraph("https://i0.hdslb.com/p.jpg", 8, 4), wantMedia: 1},
		{name: "divider skipped", paragraph: `{"para_type":3,"line":{"pic":{"url":"https://i0.hdslb.com/line.png"}}}`},
		{name: "link card skipped", paragraph: `{"para_type":6,"link_card":{"card":{"type":"LINK_CARD_TYPE_GOODS"}}}`},
		{
			name:      "rich link",
			paragraph: `{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_RICH","rich":{"orig_text":"话题","jump_url":"//www.bilibili.com/v/topic/detail"}}]}}`,
			wantBody:  "话题",
			wantLink:  "https://www.bilibili.com/v/topic/detail",
		},
		{name: "formula", paragraph: `{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_FORMULA","formula":{"latex_content":"e=mc^2"}}]}}`, wantBody: "e=mc^2"},
		{name: "quoted para type", paragraph: `{"para_type":"1","text":{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":"quoted"}}]}}`, wantBody: "quoted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseOpusDetail("1", []byte(opusDetailEnvelope(opusDetailItem("1", "t", []string{tt.paragraph, opusTextParagraph("tail")}))))
			require.NoError(t, err)
			if tt.wantBody == "" && tt.wantMedia == 0 && tt.wantLink == "" {
				assert.Equal(t, "tail", got.Body)
				assert.Empty(t, got.Media)
				return
			}
			if tt.wantMedia > 0 {
				require.Len(t, got.Media, tt.wantMedia)
				assert.Equal(t, "tail", got.Body)
				return
			}
			if tt.wantBody != "" {
				assert.Equal(t, tt.wantBody+"\n\ntail", got.Body)
			}
			if tt.wantLink != "" {
				require.Len(t, got.Links, 1)
				assert.Equal(t, tt.wantLink, got.Links[0].URL)
			}
		})
	}
}

func TestParseOpusDetailErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      string
		body    string
		want    string
		wantNil bool
	}{
		{name: "empty id", want: "opus id is required", wantNil: true},
		{name: "invalid envelope", id: "1", body: `{`, want: "invalid JSON envelope"},
		{name: "missing item", id: "1", body: `{"code":0,"data":{}}`, want: "missing item"},
		{name: "root fallback", id: "1", body: `{"code":0,"data":{"fallback":{"id":12,"type":2}}}`, want: "fallback"},
		{name: "item fallback", id: "1", body: `{"code":0,"data":{"item":{"id_str":"1","fallback":1,"modules":[]}}}`, want: "fallback"},
		{name: "id mismatch", id: "1", body: opusDetailEnvelope(opusDetailItem("2", "t", []string{opusTextParagraph("x")})), want: "does not match"},
		{name: "missing modules", id: "1", body: `{"code":0,"data":{"item":{"id_str":"1"}}}`, want: "missing modules"},
		{name: "missing module type", id: "1", body: `{"code":0,"data":{"item":{"id_str":"1","modules":[{}]}}}`, want: "missing type"},
		{name: "missing content", id: "1", body: `{"code":0,"data":{"item":{"id_str":"1","modules":[{"module_type":"MODULE_TYPE_TITLE","module_title":{"text":"t"}}]}}}`, want: "MODULE_TYPE_CONTENT"},
		{name: "empty paragraphs", id: "1", body: opusDetailEnvelope(`{"id_str":"1","modules":[{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[]}}]}`), want: "no paragraphs"},
		{name: "unknown paragraph", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":99}`})), want: "unsupported opus paragraph type 99"},
		{name: "unknown text node", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_NEW"}]}}`})), want: "unsupported opus text node type"},
		{name: "missing word", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_WORD"}]}}`})), want: "missing word"},
		{name: "missing text", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":1}`})), want: "missing text"},
		{name: "missing quote text", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":4}`})), want: "missing text"},
		{name: "missing rich", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_RICH"}]}}`})), want: "missing rich"},
		{name: "missing formula", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_FORMULA"}]}}`})), want: "missing formula"},
		{name: "missing node type", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":1,"text":{"nodes":[{}]}}`})), want: "missing type"},
		{name: "invalid title", id: "1", body: `{"code":0,"data":{"item":{"id_str":"1","modules":[{"module_type":"MODULE_TYPE_TITLE","module_title":"bad"},{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":"x"}}]}}]}}]}}}`, want: "invalid opus title"},
		{name: "invalid content", id: "1", body: `{"code":0,"data":{"item":{"id_str":"1","modules":[{"module_type":"MODULE_TYPE_CONTENT","module_content":"bad"}]}}}`, want: "invalid opus content"},
		{name: "missing pic", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":2}`})), want: "missing pic"},
		{name: "empty pic", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":2,"pic":{"pics":[{"url":"not-a-url"}]}}`})), want: "no usable images"},
		{name: "missing list", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":5}`})), want: "missing list"},
		{name: "missing code", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":7}`})), want: "missing code"},
		{name: "authentication", id: "1", body: `{"code":-101,"message":"login required"}`, want: "login required"},
		{name: "risk control", id: "1", body: `{"code":-352,"message":"blocked"}`, want: "blocked"},
		{name: "empty after skip", id: "1", body: opusDetailEnvelope(opusDetailItem("1", "t", []string{`{"para_type":3}`})), want: "no text or images"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tt.wantNil {
				_, err = parseOpusDetail(tt.id, nil)
				if tt.id == "" {
					_, err = New(nil, "test").fetchOpusDetail(t.Context(), "")
				}
			} else {
				_, err = parseOpusDetail(tt.id, []byte(tt.body))
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			switch tt.name {
			case "authentication":
				assert.Equal(t, ErrorAuthentication, apiErr.Kind)
			case "risk control":
				assert.Equal(t, ErrorRiskControl, apiErr.Kind)
			default:
				assert.Equal(t, ErrorSchema, apiErr.Kind)
			}
		})
	}
}

func TestEnrichArticleNilDynamic(t *testing.T) {
	t.Parallel()
	err := New(nil, "test").EnrichArticle(t.Context(), nil)
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, ErrorSchema, apiErr.Kind)
	assert.Contains(t, apiErr.Message, "required")
}

func TestEnrichArticleMissingID(t *testing.T) {
	t.Parallel()
	err := New(nil, "test").EnrichArticle(t.Context(), &model.Dynamic{Type: "DYNAMIC_TYPE_ARTICLE"})
	require.Error(t, err)
	assert.True(t, errors.As(err, new(*APIError)))
	assert.Contains(t, err.Error(), "missing id")
}

func TestSummaryLooksLikeTruncation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		summary string
		body    string
		want    bool
	}{
		{name: "empty summary", body: "full", want: true},
		{name: "ellipsis", summary: "第一段...", body: "第一段和第二段", want: true},
		{name: "unicode ellipsis", summary: "第一段…", body: "第一段和第二段", want: true},
		{name: "full match", summary: "完整正文", body: "完整正文", want: true},
		{name: "unrelated", summary: "推荐语", body: "完整正文"},
		{name: "dots only", summary: "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, summaryLooksLikeTruncation(tt.summary, tt.body))
		})
	}
}

func TestApplyOpusContentKeepsCoversWithoutBodyImages(t *testing.T) {
	t.Parallel()
	dynamic := model.Dynamic{
		Title: "old", Description: "截断",
		Media: []model.DynamicMedia{{Kind: model.DynamicMediaCover, URL: "https://i0.hdslb.com/cover.jpg"}},
	}
	applyOpusContent(&dynamic, opusContent{Title: "new", Body: "完整正文"})
	assert.Equal(t, "new", dynamic.Title)
	assert.Equal(t, "完整正文", dynamic.Description)
	require.Len(t, dynamic.Media, 1)
	assert.Equal(t, model.DynamicMediaCover, dynamic.Media[0].Kind)
}

func TestParseOpusDetailImageOnlyArticle(t *testing.T) {
	t.Parallel()
	got, err := parseOpusDetail("1", []byte(opusDetailEnvelope(opusDetailItem("1", "t", []string{opusPicParagraph("https://i0.hdslb.com/only.jpg", 3, 4)}))))
	require.NoError(t, err)
	assert.Empty(t, got.Body)
	require.Len(t, got.Media, 1)
	assert.Equal(t, 3, got.Media[0].Width)
	assert.Equal(t, 4, got.Media[0].Height)
}

func TestParseOpusDetailDedupesMediaAndLinks(t *testing.T) {
	t.Parallel()
	got, err := parseOpusDetail("1", []byte(opusDetailEnvelope(opusDetailItem("1", "t", []string{
		opusPicParagraph("https://i0.hdslb.com/a.jpg", 1, 1),
		opusPicParagraph("https://i0.hdslb.com/a.jpg", 2, 2),
		`{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_RICH","rich":{"orig_text":"A","jump_url":"https://www.bilibili.com/a"}}]}}`,
		`{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_RICH","rich":{"orig_text":"B","jump_url":"https://www.bilibili.com/a"}}]}}`,
	}))))
	require.NoError(t, err)
	require.Len(t, got.Media, 1)
	require.Len(t, got.Links, 1)
}

func TestFetchOpusDetailClassifiesHTTPErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   int
		body     string
		wantKind ErrorKind
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantKind: ErrorAuthentication},
		{name: "risk", status: http.StatusTooManyRequests, wantKind: ErrorRiskControl},
		{name: "server", status: http.StatusBadGateway, wantKind: ErrorTemporary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				if tt.body != "" {
					_, _ = io.WriteString(w, tt.body)
				}
			}))
			t.Cleanup(server.Close)
			_, err := New(server.Client(), "test", WithBaseURLs(server.URL, server.URL)).fetchOpusDetail(t.Context(), "1")
			require.Error(t, err)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tt.wantKind, apiErr.Kind)
		})
	}
}

func FuzzParseOpusDetail(f *testing.F) {
	f.Add(opusDetailEnvelope(opusDetailItem("1", "t", []string{opusTextParagraph("hello"), opusPicParagraph("https://i0.hdslb.com/a.jpg", 1, 1)})))
	f.Add(`{"code":0,"data":{"fallback":{"type":2}}}`)
	f.Add(`not-json`)
	f.Add(`{}`)
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = parseOpusDetail("1", []byte(raw))
	})
}

func opusDetailEnvelope(item string) string {
	return `{"code":0,"message":"0","data":{"item":` + item + `}}`
}

func opusDetailItem(id, title string, paragraphs []string) string {
	modules := []string{
		`{"module_type":"MODULE_TYPE_TITLE","module_title":{"text":` + mustJSONValue(title) + `}}`,
		`{"module_type":"MODULE_TYPE_AUTHOR","module_author":{"name":"up"}}`,
		`{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[` + strings.Join(paragraphs, ",") + `]}}`,
		`{"module_type":"MODULE_TYPE_STAT","module_stat":{"like":{"count":1}}}`,
	}
	return `{"id_str":` + mustJSONValue(id) + `,"basic":{"title":` + mustJSONValue(title+" - 哔哩哔哩") + `},"modules":[` + strings.Join(modules, ",") + `]}`
}

func opusTextParagraph(text string) string {
	return `{"para_type":1,"text":{"nodes":[{"type":"TEXT_NODE_TYPE_WORD","word":{"words":` + mustJSONValue(text) + `}}]}}`
}

func opusPicParagraph(rawURL string, width, height int) string {
	return `{"para_type":2,"pic":{"pics":[{"url":` + mustJSONValue(rawURL) + `,"width":` + strconv.Itoa(width) + `,"height":` + strconv.Itoa(height) + `}]}}`
}

func mustJSONValue(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
