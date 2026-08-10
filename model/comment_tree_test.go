package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCommentTreeMalformedRelationships(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		nodes      []CommentNode
		wantRoots  []string
		incomplete bool
	}{
		{name: "nested tree", nodes: []CommentNode{{ID: "root", Time: now}, {ID: "child", ParentID: "root", Time: now.Add(time.Second)}}, wantRoots: []string{"root"}},
		{name: "orphan remains visible", nodes: []CommentNode{{ID: "child", ParentID: "missing", Time: now}}, wantRoots: []string{"child"}, incomplete: true},
		{name: "cycle is broken", nodes: []CommentNode{{ID: "a", ParentID: "b", Time: now}, {ID: "b", ParentID: "a", Time: now.Add(time.Second)}}, wantRoots: []string{"a", "b"}, incomplete: true},
		{name: "empty identity is rejected", nodes: []CommentNode{{Time: now}}, wantRoots: []string{}, incomplete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			roots, incomplete := BuildCommentTree(tt.nodes)
			ids := make([]string, 0, len(roots))
			for _, root := range roots {
				ids = append(ids, root.ID)
			}
			assert.Equal(t, tt.wantRoots, ids)
			assert.Equal(t, tt.incomplete, incomplete)
			if tt.name == "nested tree" {
				require.Len(t, roots, 1)
				require.Len(t, roots[0].Children, 1)
				assert.Equal(t, "child", roots[0].Children[0].ID)
			}
		})
	}
}

func TestCommentAncestorPath(t *testing.T) {
	t.Parallel()
	path, complete := CommentAncestorPath([]CommentNode{{ID: "root"}, {ID: "middle", ParentID: "root"}, {ID: "leaf", ParentID: "middle"}}, "leaf")
	require.True(t, complete)
	require.Len(t, path, 3)
	assert.Equal(t, []string{"root", "middle", "leaf"}, []string{path[0].ID, path[1].ID, path[2].ID})
}
