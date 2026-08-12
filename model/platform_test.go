package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceValidatesZSXQTopicFilters(t *testing.T) {
	t.Parallel()
	base := Source{ID: SourceID(PlatformZSXQ, "9"), Platform: PlatformZSXQ, Type: SourceZSXQPlanet, ExternalID: "9", ZSXQTopicMode: ZSXQTopicAll}
	tests := []struct {
		name    string
		mutate  func(*Source)
		wantErr string
	}{
		{name: "all topics", mutate: func(*Source) {}},
		{name: "multiple selected authors", mutate: func(source *Source) {
			source.ZSXQTopicMode = ZSXQTopicSelectedAuthors
			source.ZSXQAuthors = []ZSXQAuthor{{UserID: "8", Name: "Owner"}, {UserID: "9"}}
		}},
		{name: "selected list empty", mutate: func(source *Source) { source.ZSXQTopicMode = ZSXQTopicSelectedAuthors }, wantErr: "at least one"},
		{name: "all has authors", mutate: func(source *Source) { source.ZSXQAuthors = []ZSXQAuthor{{UserID: "8"}} }, wantErr: "cannot have"},
		{name: "duplicate author", mutate: func(source *Source) {
			source.ZSXQTopicMode = ZSXQTopicSelectedAuthors
			source.ZSXQAuthors = []ZSXQAuthor{{UserID: "8"}, {UserID: "8"}}
		}, wantErr: "duplicate"},
		{name: "invalid author", mutate: func(source *Source) {
			source.ZSXQTopicMode = ZSXQTopicSelectedAuthors
			source.ZSXQAuthors = []ZSXQAuthor{{UserID: "08"}}
		}, wantErr: "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := base
			tt.mutate(&source)
			err := source.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
