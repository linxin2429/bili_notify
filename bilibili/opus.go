package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/linxin2429/bili_notify/model"
)

type opusContent struct {
	Title string
	Body  string
	Media []model.DynamicMedia
	Links []model.DynamicLink
}

type rawOpusDetailData struct {
	Item     *rawOpusItem    `json:"item"`
	Fallback json.RawMessage `json:"fallback"`
}

type rawOpusItem struct {
	ID       string          `json:"id_str"`
	Basic    *rawOpusBasic   `json:"basic"`
	Modules  []rawOpusModule `json:"modules"`
	Fallback json.RawMessage `json:"fallback"`
}

type rawOpusBasic struct {
	Title string `json:"title"`
}

type rawOpusModule struct {
	Type    string          `json:"module_type"`
	Title   json.RawMessage `json:"module_title"`
	Content json.RawMessage `json:"module_content"`
}

type rawOpusTitle struct {
	Text string `json:"text"`
}

type rawOpusModuleContent struct {
	Paragraphs []rawOpusParagraph `json:"paragraphs"`
}

type rawOpusParagraph struct {
	ParaType flexibleInt `json:"para_type"`
	Text     *struct {
		Nodes []rawOpusTextNode `json:"nodes"`
	} `json:"text"`
	Pic *struct {
		Pics []struct {
			URL    string      `json:"url"`
			Width  flexibleInt `json:"width"`
			Height flexibleInt `json:"height"`
		} `json:"pics"`
	} `json:"pic"`
	List *struct {
		Style flexibleInt `json:"style"`
		Items []struct {
			Nodes []rawOpusTextNode `json:"nodes"`
		} `json:"items"`
	} `json:"list"`
	Code *struct {
		Content string `json:"content"`
		Lang    string `json:"lang"`
	} `json:"code"`
}

type rawOpusTextNode struct {
	Type string `json:"type"`
	Word *struct {
		Words string `json:"words"`
	} `json:"word"`
	Rich    *rawRichTextNode `json:"rich"`
	Formula *struct {
		LatexContent string `json:"latex_content"`
	} `json:"formula"`
}

const (
	opusParaText     = 1
	opusParaPic      = 2
	opusParaLine     = 3
	opusParaQuote    = 4
	opusParaList     = 5
	opusParaLinkCard = 6
	opusParaCode     = 7
)

func articleDynamic(dynamic *model.Dynamic) bool {
	return dynamic != nil && dynamic.Type == articleDynamicType
}

// NeedsArticleEnrichment reports whether a dynamic or its forwarded original is
// a Bilibili column whose list card is only a truncated summary.
func NeedsArticleEnrichment(dynamic model.Dynamic) bool {
	if articleDynamic(&dynamic) {
		return true
	}
	if dynamic.Original != nil {
		return NeedsArticleEnrichment(*dynamic.Original)
	}
	return false
}

// EnrichArticle replaces a column card's truncated summary and covers with the
// opus detail body and inline images. Non-article dynamics are left unchanged.
func (c *Client) EnrichArticle(ctx context.Context, dynamic *model.Dynamic) error {
	if dynamic == nil {
		return &APIError{Kind: ErrorSchema, Message: "article dynamic is required"}
	}
	if articleDynamic(dynamic) {
		if err := c.enrichOneArticle(ctx, dynamic); err != nil {
			return err
		}
	}
	if dynamic.Original != nil {
		return c.EnrichArticle(ctx, dynamic.Original)
	}
	return nil
}

func (c *Client) enrichOneArticle(ctx context.Context, dynamic *model.Dynamic) error {
	id := strings.TrimSpace(dynamic.ID)
	if id == "" {
		return &APIError{Kind: ErrorSchema, Message: "article dynamic is missing id"}
	}
	content, err := c.fetchOpusDetail(ctx, id)
	if err != nil {
		return err
	}
	applyOpusContent(dynamic, content)
	return nil
}

func (c *Client) fetchOpusDetail(ctx context.Context, id string) (opusContent, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus id is required"}
	}
	if err := c.ensureDeviceCookie(ctx); err != nil {
		return opusContent{}, fmt.Errorf("preparing opus detail request: %w", err)
	}
	query := url.Values{
		"id":              {id},
		"features":        {opusDetailFeatures},
		"timezone_offset": {"-480"},
	}
	response, body, err := c.get(ctx, c.apiURL+"/x/polymer/web-dynamic/v1/opus/detail", query, true)
	if err != nil {
		return opusContent{}, fmt.Errorf("fetching opus detail: %w", err)
	}
	content, err := parseOpusDetail(id, body)
	if err != nil {
		if apiErr, ok := errors.AsType[*APIError](err); ok {
			apiErr.HTTPStatus = response.StatusCode
		}
		return opusContent{}, fmt.Errorf("opus %s detail: %w", id, err)
	}
	return content, nil
}

func parseOpusDetail(id string, body []byte) (opusContent, error) {
	var data rawOpusDetailData
	if err := decodeEnvelope(body, &data); err != nil {
		return opusContent{}, err
	}
	if data.Item == nil {
		if len(data.Fallback) > 0 && string(data.Fallback) != "null" {
			return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus detail returned a fallback instead of htmlNewStyle content"}
		}
		return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus detail is missing item"}
	}
	if data.Item.ID != "" && data.Item.ID != id {
		return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus detail id does not match the requested dynamic"}
	}
	if len(data.Item.Fallback) > 0 && string(data.Item.Fallback) != "null" && len(data.Item.Modules) == 0 {
		return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus detail returned a fallback instead of htmlNewStyle content"}
	}
	for _, module := range data.Item.Modules {
		if module.Type == "MODULE_TYPE_PAYWALL" {
			// A successful API envelope can still contain only a paid preview.
			// Keep this item pending without invalidating the whole login session.
			return opusContent{}, &APIError{Kind: ErrorContentAccess, Message: "opus detail contains a paywall; full article access was not granted"}
		}
	}
	content, err := parseOpusModules(data.Item)
	if err != nil {
		return opusContent{}, err
	}
	return content, nil
}

func parseOpusModules(item *rawOpusItem) (opusContent, error) {
	if item == nil {
		return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus detail is missing item"}
	}
	var (
		content    opusContent
		foundBody  bool
		paragraphs []string
		seenMedia  = map[string]bool{}
		seenLinks  = map[string]bool{}
	)
	if item.Basic != nil {
		content.Title = strings.TrimSpace(strings.TrimSuffix(item.Basic.Title, " - 哔哩哔哩"))
	}
	if len(item.Modules) == 0 {
		return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus detail is missing modules"}
	}
	for _, module := range item.Modules {
		if strings.TrimSpace(module.Type) == "" {
			return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus module is missing type"}
		}
		switch module.Type {
		case "MODULE_TYPE_TITLE":
			var title rawOpusTitle
			if err := json.Unmarshal(module.Title, &title); err != nil {
				return opusContent{}, &APIError{Kind: ErrorSchema, Message: "invalid opus title module: " + err.Error()}
			}
			if text := strings.TrimSpace(title.Text); text != "" {
				content.Title = text
			}
		case "MODULE_TYPE_CONTENT":
			foundBody = true
			var body rawOpusModuleContent
			if err := json.Unmarshal(module.Content, &body); err != nil {
				return opusContent{}, &APIError{Kind: ErrorSchema, Message: "invalid opus content module: " + err.Error()}
			}
			if len(body.Paragraphs) == 0 {
				return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus content module has no paragraphs"}
			}
			for _, paragraph := range body.Paragraphs {
				text, media, links, err := parseOpusParagraph(paragraph)
				if err != nil {
					return opusContent{}, err
				}
				if text != "" {
					paragraphs = append(paragraphs, text)
				}
				for _, picture := range media {
					if seenMedia[picture.URL] {
						continue
					}
					seenMedia[picture.URL] = true
					content.Media = append(content.Media, picture)
				}
				for _, link := range links {
					if seenLinks[link.URL] {
						continue
					}
					seenLinks[link.URL] = true
					content.Links = append(content.Links, link)
				}
			}
		}
	}
	if !foundBody {
		return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus detail is missing MODULE_TYPE_CONTENT"}
	}
	content.Body = strings.Join(paragraphs, "\n\n")
	if content.Body == "" && len(content.Media) == 0 {
		return opusContent{}, &APIError{Kind: ErrorSchema, Message: "opus content module produced no text or images"}
	}
	return content, nil
}

func parseOpusParagraph(paragraph rawOpusParagraph) (string, []model.DynamicMedia, []model.DynamicLink, error) {
	switch int(paragraph.ParaType) {
	case opusParaText:
		if paragraph.Text == nil {
			return "", nil, nil, &APIError{Kind: ErrorSchema, Message: "opus text paragraph is missing text"}
		}
		text, links, err := parseOpusTextNodes(paragraph.Text.Nodes)
		if err != nil {
			return "", nil, nil, err
		}
		return text, nil, links, nil
	case opusParaPic:
		if paragraph.Pic == nil {
			return "", nil, nil, &APIError{Kind: ErrorSchema, Message: "opus picture paragraph is missing pic"}
		}
		media := make([]model.DynamicMedia, 0, len(paragraph.Pic.Pics))
		for _, picture := range paragraph.Pic.Pics {
			pictureURL := webURL(picture.URL)
			if pictureURL == "" {
				continue
			}
			media = append(media, model.DynamicMedia{
				Kind:   model.DynamicMediaImage,
				URL:    pictureURL,
				Width:  int(picture.Width),
				Height: int(picture.Height),
			})
		}
		if len(media) == 0 {
			return "", nil, nil, &APIError{Kind: ErrorSchema, Message: "opus picture paragraph has no usable images"}
		}
		return "", media, nil, nil
	case opusParaLine, opusParaLinkCard:
		return "", nil, nil, nil
	case opusParaQuote:
		if paragraph.Text == nil {
			return "", nil, nil, &APIError{Kind: ErrorSchema, Message: "opus quote paragraph is missing text"}
		}
		text, links, err := parseOpusTextNodes(paragraph.Text.Nodes)
		if err != nil {
			return "", nil, nil, err
		}
		if text == "" {
			return "", nil, links, nil
		}
		return quoteParagraph(text), nil, links, nil
	case opusParaList:
		if paragraph.List == nil {
			return "", nil, nil, &APIError{Kind: ErrorSchema, Message: "opus list paragraph is missing list"}
		}
		lines := make([]string, 0, len(paragraph.List.Items))
		var links []model.DynamicLink
		ordered := int(paragraph.List.Style) == 1
		for i, item := range paragraph.List.Items {
			text, itemLinks, err := parseOpusTextNodes(item.Nodes)
			if err != nil {
				return "", nil, nil, err
			}
			if text == "" {
				continue
			}
			if ordered {
				lines = append(lines, strconv.Itoa(i+1)+". "+text)
			} else {
				lines = append(lines, "- "+text)
			}
			links = append(links, itemLinks...)
		}
		return strings.Join(lines, "\n"), nil, links, nil
	case opusParaCode:
		if paragraph.Code == nil {
			return "", nil, nil, &APIError{Kind: ErrorSchema, Message: "opus code paragraph is missing code"}
		}
		return unescapeOpusText(paragraph.Code.Content), nil, nil, nil
	default:
		return "", nil, nil, &APIError{Kind: ErrorSchema, Message: fmt.Sprintf("unsupported opus paragraph type %d", int(paragraph.ParaType))}
	}
}

func parseOpusTextNodes(nodes []rawOpusTextNode) (string, []model.DynamicLink, error) {
	var (
		b     strings.Builder
		links []model.DynamicLink
	)
	for _, node := range nodes {
		switch node.Type {
		case "TEXT_NODE_TYPE_WORD":
			if node.Word == nil {
				return "", nil, &APIError{Kind: ErrorSchema, Message: "opus word node is missing word"}
			}
			b.WriteString(unescapeOpusText(node.Word.Words))
		case "TEXT_NODE_TYPE_RICH":
			if node.Rich == nil {
				return "", nil, &APIError{Kind: ErrorSchema, Message: "opus rich node is missing rich"}
			}
			text := unescapeOpusText(node.Rich.OrigText)
			if strings.TrimSpace(text) == "" {
				text = unescapeOpusText(node.Rich.Text)
			}
			b.WriteString(text)
			if link := webURL(node.Rich.JumpURL); link != "" {
				label := strings.TrimSpace(text)
				if label == "" {
					label = "正文链接"
				}
				links = append(links, model.DynamicLink{Text: label, URL: link})
			}
		case "TEXT_NODE_TYPE_FORMULA":
			if node.Formula == nil {
				return "", nil, &APIError{Kind: ErrorSchema, Message: "opus formula node is missing formula"}
			}
			b.WriteString(unescapeOpusText(node.Formula.LatexContent))
		default:
			if node.Type == "" {
				return "", nil, &APIError{Kind: ErrorSchema, Message: "opus text node is missing type"}
			}
			return "", nil, &APIError{Kind: ErrorSchema, Message: "unsupported opus text node type " + node.Type}
		}
	}
	return strings.TrimSpace(b.String()), links, nil
}

func quoteParagraph(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

func unescapeOpusText(value string) string {
	return html.UnescapeString(value)
}

func applyOpusContent(dynamic *model.Dynamic, content opusContent) {
	if content.Title != "" {
		dynamic.Title = content.Title
	}
	if content.Body != "" {
		if summaryLooksLikeTruncation(dynamic.Summary, content.Body) {
			dynamic.Summary = ""
		}
		dynamic.Description = content.Body
	}
	if len(content.Media) > 0 {
		dynamic.Media = content.Media
	}
	if len(content.Links) == 0 {
		return
	}
	seen := make(map[string]bool, len(dynamic.Links)+len(content.Links))
	for _, link := range dynamic.Links {
		seen[link.URL] = true
	}
	for _, link := range content.Links {
		if seen[link.URL] {
			continue
		}
		seen[link.URL] = true
		dynamic.Links = append(dynamic.Links, link)
	}
}

func summaryLooksLikeTruncation(summary, body string) bool {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return true
	}
	trimmed := summary
	for {
		next := strings.TrimRight(trimmed, " ")
		switch {
		case strings.HasSuffix(next, "......"):
			trimmed = strings.TrimSuffix(next, "......")
		case strings.HasSuffix(next, "..."):
			trimmed = strings.TrimSuffix(next, "...")
		case strings.HasSuffix(next, "…"):
			trimmed = strings.TrimSuffix(next, "…")
		default:
			trimmed = strings.TrimRight(next, ".")
			if trimmed == next {
				if trimmed == "" {
					return false
				}
				return strings.HasPrefix(strings.TrimSpace(body), trimmed)
			}
		}
	}
}
