package service

import (
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIndexLiveComments(t *testing.T) {
	t.Parallel()
	deletedAt := time.Unix(1700000100, 0)
	tests := []struct {
		name      string
		nodes     []model.CommentNode
		wantRoots []string
		wantKids  map[string]int
	}{
		{name: "empty"},
		{
			name:      "only roots",
			nodes:     []model.CommentNode{{RPID: "root-a"}, {RPID: "root-b"}},
			wantRoots: []string{"root-a", "root-b"},
			wantKids:  map[string]int{"root-a": 0, "root-b": 0},
		},
		{
			name: "live versus tombstoned children",
			nodes: []model.CommentNode{
				{ID: "bilibili:comment:root", RPID: "root"},
				{ID: "bilibili:comment:kept", RPID: "kept", RootID: "bilibili:comment:root", ParentID: "bilibili:comment:root"},
				{ID: "bilibili:comment:gone", RPID: "gone", RootID: "bilibili:comment:root", ParentID: "bilibili:comment:root", DeletedAt: deletedAt},
			},
			wantRoots: []string{"root"},
			wantKids:  map[string]int{"root": 1},
		},
		{
			name: "orphan counts under root id",
			nodes: []model.CommentNode{
				{RPID: "root"},
				{RPID: "orphan", RootID: "bilibili:comment:root", ParentID: "bilibili:comment:missing"},
			},
			wantRoots: []string{"root"},
			wantKids:  map[string]int{"root": 1},
		},
		{
			name: "nested tree flattens",
			nodes: []model.CommentNode{{
				RPID: "root",
				Children: []model.CommentNode{{
					RPID: "child", RootID: "bilibili:comment:root", ParentID: "bilibili:comment:root",
					Children: []model.CommentNode{{RPID: "grand", RootID: "bilibili:comment:root", ParentID: "bilibili:comment:child"}},
				}},
			}},
			wantRoots: []string{"root"},
			wantKids:  map[string]int{"root": 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			idx := indexLiveComments(tt.nodes)
			require.Len(t, idx, len(tt.wantRoots))
			for _, root := range tt.wantRoots {
				stat, ok := idx[root]
				require.True(t, ok, root)
				assert.Equal(t, tt.wantKids[root], stat.LiveChildren)
			}
		})
	}
}

func TestExpandReasonFor(t *testing.T) {
	t.Parallel()
	idx := indexLiveComments([]model.CommentNode{
		{RPID: "old-root"},
		{RPID: "kept", RootID: "bilibili:comment:old-root", ParentID: "bilibili:comment:old-root"},
	})
	baseline := walkKindFor(model.CommentTarget{}, nil)
	ready := walkKindFor(model.CommentTarget{BaselineReady: true}, idx)
	tests := []struct {
		name string
		root bilibili.Reply
		kind walkKind
		want expandReason
	}{
		{name: "baseline populated", root: bilibili.Reply{RPID: "old-root", RCount: 1}, kind: baseline, want: expandBaseline},
		{name: "incremental known equal", root: bilibili.Reply{RPID: "old-root", RCount: 1}, kind: ready, want: expandNone},
		{name: "growth", root: bilibili.Reply{RPID: "old-root", RCount: 2}, kind: ready, want: expandChildGrowth},
		{name: "new root", root: bilibili.Reply{RPID: "fresh", RCount: 1}, kind: ready, want: expandNewRoot},
		{name: "rcount zero", root: bilibili.Reply{RPID: "fresh", RCount: 0}, kind: ready, want: expandNone},
		{name: "preview unknown", root: bilibili.Reply{RPID: "old-root", RCount: 1, Preview: []bilibili.Reply{{RPID: "unseen"}}}, kind: ready, want: expandPreviewUnknown},
		{name: "preview already stored", root: bilibili.Reply{RPID: "old-root", RCount: 1, Preview: []bilibili.Reply{{RPID: "kept"}}}, kind: ready, want: expandNone},
		{name: "shrink expands", root: bilibili.Reply{RPID: "old-root", RCount: 0}, kind: ready, want: expandChildGrowth},
		{name: "ready with empty archive rebases", root: bilibili.Reply{RPID: "old-root", RCount: 1}, kind: walkKindFor(model.CommentTarget{BaselineReady: true}, nil), want: expandBaseline},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, expandReasonFor(tt.root, idx, tt.kind))
		})
	}
}

func TestAssembleCommentSnapshot(t *testing.T) {
	t.Parallel()
	stored := []model.CommentNode{
		{ID: "bilibili:comment:kept-root", RPID: "kept-root", Time: time.Unix(1, 0)},
		{ID: "bilibili:comment:kept", RPID: "kept", RootID: "bilibili:comment:kept-root", ParentID: "bilibili:comment:kept-root", Time: time.Unix(2, 0)},
		{ID: "bilibili:comment:vanished-root", RPID: "vanished-root", Time: time.Unix(3, 0)},
		{ID: "bilibili:comment:vanished-child", RPID: "vanished-child", RootID: "bilibili:comment:vanished-root", ParentID: "bilibili:comment:vanished-root", Time: time.Unix(4, 0)},
		{ID: "bilibili:comment:grown-root", RPID: "grown-root", Time: time.Unix(5, 0)},
		{ID: "bilibili:comment:stale-child", RPID: "stale-child", RootID: "bilibili:comment:grown-root", ParentID: "bilibili:comment:grown-root", Time: time.Unix(6, 0)},
	}
	tests := []struct {
		name     string
		in       assembleInput
		wantIDs  []string
		omitIDs  []string
		complete bool
	}{
		{
			name: "exhaustive graft keeps ungrown children",
			in: assembleInput{
				ContentID: "bilibili:content:dynamic",
				UPUID:     "42",
				Stored:    stored,
				Fetched: fetchedComments{
					Roots: map[string]bilibili.Reply{
						"kept-root":  {RPID: "kept-root", Mid: "7", CTime: time.Unix(1, 0)},
						"grown-root": {RPID: "grown-root", Mid: "7", CTime: time.Unix(5, 0), RCount: 1},
					},
					Children: map[string]bilibili.Reply{
						"fresh-child": {RPID: "fresh-child", Root: "grown-root", Parent: "grown-root", Mid: "42", CTime: time.Unix(7, 0)},
					},
				},
				Walk: commentWalk{
					Expand:          map[string]expandReason{"grown-root": expandChildGrowth},
					RootsExhaustive: true,
					ChildExhaustive: map[string]bool{"grown-root": true},
				},
			},
			wantIDs:  []string{"kept-root", "kept", "grown-root", "fresh-child"},
			omitIDs:  []string{"vanished-child", "vanished-root", "stale-child"},
			complete: true,
		},
		{
			name: "truncated roots are incomplete",
			in: assembleInput{
				ContentID: "bilibili:content:dynamic",
				UPUID:     "42",
				Fetched:   fetchedComments{Roots: map[string]bilibili.Reply{"kept-root": {RPID: "kept-root"}}},
				Walk:      commentWalk{RootsExhaustive: false, Expand: map[string]expandReason{}, ChildExhaustive: map[string]bool{}},
			},
			wantIDs:  []string{"kept-root"},
			complete: false,
		},
		{
			name: "truncated expand is incomplete",
			in: assembleInput{
				ContentID: "bilibili:content:dynamic",
				UPUID:     "42",
				Fetched: fetchedComments{
					Roots:    map[string]bilibili.Reply{"grown-root": {RPID: "grown-root", RCount: 2}},
					Children: map[string]bilibili.Reply{"fresh-child": {RPID: "fresh-child", Root: "grown-root", Parent: "grown-root"}},
				},
				Walk: commentWalk{
					Expand:          map[string]expandReason{"grown-root": expandChildGrowth},
					RootsExhaustive: true,
					ChildExhaustive: map[string]bool{"grown-root": false},
				},
			},
			wantIDs:  []string{"grown-root", "fresh-child"},
			complete: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snap := assembleCommentSnapshot(tt.in)
			assert.Equal(t, tt.complete, snap.Complete())
			got := commentRPIDs(snap.Nodes())
			for _, id := range tt.wantIDs {
				assert.Contains(t, got, id)
			}
			for _, id := range tt.omitIDs {
				assert.NotContains(t, got, id)
			}
			_, malformed := model.BuildCommentTree(snap.Nodes())
			assert.False(t, malformed)
		})
	}
}

func TestCommentWalkReady(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		walk    commentWalk
		wantErr error
	}{
		{
			name: "exhaustive",
			walk: commentWalk{RootsExhaustive: true, Expand: map[string]expandReason{"grown": expandChildGrowth}, ChildExhaustive: map[string]bool{"grown": true}},
		},
		{
			name:    "truncated roots",
			walk:    commentWalk{RootsExhaustive: false, Expand: map[string]expandReason{}, ChildExhaustive: map[string]bool{}},
			wantErr: errCommentWalkIncomplete,
		},
		{
			name:    "truncated expand",
			walk:    commentWalk{RootsExhaustive: true, Expand: map[string]expandReason{"grown": expandChildGrowth}, ChildExhaustive: map[string]bool{"grown": false}},
			wantErr: errCommentWalkIncomplete,
		},
		{
			name:    "missing child exhaustive flag",
			walk:    commentWalk{RootsExhaustive: true, Expand: map[string]expandReason{"grown": expandChildGrowth}, ChildExhaustive: map[string]bool{}},
			wantErr: errCommentWalkIncomplete,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := commentWalkReady(tt.walk)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func commentRPIDs(nodes []model.CommentNode) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.RPID)
	}
	return ids
}
