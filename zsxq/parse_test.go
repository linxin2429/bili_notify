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
