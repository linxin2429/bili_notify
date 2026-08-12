package zsxq

import (
	"encoding/json"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTopicTypesAndStrictUnknowns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		topic    apiTopic
		wantType model.ContentType
		wantErr  error
	}{
		{name: "discussion", topic: topicFixture("talk", &apiBody{Owner: userFixture(), Text: "hello <script>"}), wantType: model.ContentDiscussion},
		{name: "question and answer", topic: func() apiTopic {
			topic := topicFixture("question", &apiBody{Owner: userFixture(), Text: "question"})
			topic.Answer = &apiBody{Owner: userFixture(), Text: "answer"}
			return topic
		}(), wantType: model.ContentQuestion},
		{name: "task", topic: topicFixture("task", &apiBody{Owner: userFixture(), Text: "task"}), wantType: model.ContentTask},
		{name: "long article", topic: topicFixture("article", &apiBody{Owner: userFixture(), Text: "article"}), wantType: model.ContentLongArticle},
		{name: "unknown is rejected", topic: topicFixture("new_type", &apiBody{Owner: userFixture()}), wantErr: ErrSchemaDrift},
		{name: "missing body is rejected", topic: topicFixture("talk", nil), wantErr: ErrSchemaDrift},
	}
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "9"), Platform: model.PlatformZSXQ, Type: model.SourceZSXQPlanet, ExternalID: "9"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content, _, err := parseTopic(source, tt.topic)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, content.Type)
			assert.NotContains(t, content.SafeHTML, "<script>")
			if tt.name == "question and answer" {
				assert.Contains(t, content.Text, "回答")
			}
		})
	}
}

func TestParseCommentUsesOnlyPlanetOwnerAsNotificationRole(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user apiUser
		want model.AuthorRole
	}{
		{name: "planet owner", user: apiUser{UserID: json.Number("8"), Name: "Owner", Role: "owner"}, want: model.RoleOwner},
		{name: "administrator", user: apiUser{UserID: json.Number("7"), Name: "Admin", Role: "admin"}, want: model.RoleAdmin},
		{name: "ordinary author cannot claim owner role", user: apiUser{UserID: json.Number("6"), Name: "Member", Role: "owner"}, want: model.RoleMember},
	}
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "1"), Platform: model.PlatformZSXQ, ExternalID: "1"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			node, err := parseComment(content, "8", apiComment{CommentID: json.Number("3"), CreateTime: "2026-08-10T00:00:00Z", Owner: tt.user})
			require.NoError(t, err)
			assert.Equal(t, tt.want, node.Role)
		})
	}
}

func TestParseCurrentTopicShape(t *testing.T) {
	t.Parallel()
	source := model.Source{ID: model.SourceID(model.PlatformZSXQ, "28882581855851"), Platform: model.PlatformZSXQ,
		Type: model.SourceZSXQPlanet, ExternalID: "28882581855851", OwnerID: "548818848124544"}
	topic := apiTopic{
		TopicID: json.Number("22255155254188541"), Type: "talk", Title: "SemiAnalysis的NV...",
		CreateTime: "2026-08-12T14:55:00.479+0800", LikesCount: 5, CommentsCount: 1, RewardsCount: 0,
		Talk: &apiBody{Owner: apiUser{UserID: json.Number("548818848124544"), Name: "小小"},
			Text:  `SemiAnalysis的NVL576 <e type="hashtag" hid="15584424444552" title="%23SemiAnalysis%23" />`,
			Files: []apiFile{{FileID: json.Number("814511428244812"), Name: "Rubin Ultra NVL576 架构：快速概览.pdf", Size: 6483608}}},
		ShowComments: []apiComment{{CommentID: json.Number("4842482254125488"), CreateTime: "2026-08-12T21:53:56.094+0800",
			Owner: apiUser{UserID: json.Number("218584241484151"), Name: "李明阳"}, Text: "test"}},
	}

	content, attachments, err := parseTopic(source, topic)
	require.NoError(t, err)
	comments, complete, err := parseShownComments(content, source.OwnerID, topic.ShowComments, topic.CommentsCount)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	require.Len(t, comments, 1)
	assert.Empty(t, content.Title)
	assert.Equal(t, "SemiAnalysis的NVL576 #SemiAnalysis#", content.Text)
	assert.Equal(t, "SemiAnalysis的NVL576 #SemiAnalysis#", content.SafeHTML)
	assert.Equal(t, int64(6483608), attachments[0].Size)
	assert.Equal(t, "Rubin Ultra NVL576 架构：快速概览.pdf", attachments[0].FileName)
	assert.Equal(t, "test", comments[0].Message)
	assert.True(t, complete)
}

func TestRenderRichText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "hashtag", input: `<e type="hashtag" hid="1" title="%23SemiAnalysis%23" />`, want: "#SemiAnalysis#"},
		{name: "mention", input: `<e type='mention' uid='1' title='%40%E5%B0%8F%E5%B0%8F' />`, want: "@小小"},
		{name: "bold text", input: `<e type="text_bold" title="important%20text" />`, want: "important text"},
		{name: "line breaks", input: "first<br />second<BR>third", want: "first\nsecond\nthird"},
		{name: "unsafe HTML remains plain text", input: `<script>alert(1)</script>`, want: `<script>alert(1)</script>`},
		{name: "malformed percent encoding is preserved", input: `<e type="mention" title="bad%value" />`, want: "bad%value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, renderRichText(tt.input))
			assert.NotContains(t, sanitizeRichText(tt.input), "<script>")
		})
	}
}

func TestShownCommentCompleteness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		total    int64
		comments []apiComment
		want     bool
	}{
		{name: "no comments", total: 0, want: true},
		{name: "all comments shown", total: 1, comments: []apiComment{{CommentID: json.Number("1"), CreateTime: "2026-08-12T00:00:00Z", Owner: userFixture()}}, want: true},
		{name: "only preview comments shown", total: 2, comments: []apiComment{{CommentID: json.Number("1"), CreateTime: "2026-08-12T00:00:00Z", Owner: userFixture()}}, want: false},
	}
	content := model.Content{ID: model.ContentID(model.PlatformZSXQ, "1"), Platform: model.PlatformZSXQ, ExternalID: "1"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, complete, err := parseShownComments(content, "8", tt.comments, tt.total)
			require.NoError(t, err)
			assert.Equal(t, tt.want, complete)
		})
	}
}

func topicFixture(kind string, body *apiBody) apiTopic {
	topic := apiTopic{TopicID: json.Number("1"), Type: kind, CreateTime: "2026-08-10T00:00:00Z"}
	switch kind {
	case "talk":
		topic.Talk = body
	case "question":
		topic.Question = body
	case "task":
		topic.Task = body
	case "article":
		topic.Article = body
	}
	return topic
}

func userFixture() apiUser { return apiUser{UserID: json.Number("8"), Name: "Owner"} }
