package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/linxin2429/bili_notify/bilibili"
	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/state"
)

var errCommentWalkIncomplete = errors.New("comment walk incomplete")

type walkKind struct {
	baseline bool
}

func walkKindFor(target model.CommentTarget) walkKind {
	return walkKind{baseline: !target.BaselineReady}
}

func (k walkKind) expandEveryPopulatedRoot() bool { return k.baseline }

type liveRootIndex map[string]liveRootStat

type liveRootStat struct {
	Root         model.CommentNode
	LiveChildren int
	ChildIDs     map[string]struct{}
}

type expandReason int

const (
	expandNone expandReason = iota
	expandBaseline
	expandNewRoot
	expandChildGrowth
	expandPreviewUnknown
)

type fetchedComments struct {
	Roots    map[string]bilibili.Reply
	Children map[string]bilibili.Reply
}

type commentWalk struct {
	Expand          map[string]expandReason
	RootsExhaustive bool
	ChildExhaustive map[string]bool
}

type commentSnapshot struct {
	nodes    []model.CommentNode
	complete bool
}

func (s commentSnapshot) Nodes() []model.CommentNode { return s.nodes }
func (s commentSnapshot) Complete() bool             { return s.complete }

type assembleInput struct {
	ContentID string
	UPUID     string
	Stored    []model.CommentNode
	Fetched   fetchedComments
	Walk      commentWalk
}

func indexLiveComments(nodes []model.CommentNode) liveRootIndex {
	nodes = flattenCommentNodes(nodes)
	idx := make(liveRootIndex)
	for _, node := range nodes {
		if !isLiveComment(node) || !isCommentRoot(node) {
			continue
		}
		rpid := commentRPID(node)
		if rpid == "" {
			continue
		}
		idx[rpid] = liveRootStat{Root: node, ChildIDs: make(map[string]struct{})}
	}
	for _, node := range nodes {
		if !isLiveComment(node) || isCommentRoot(node) {
			continue
		}
		rootRPID := commentRPIDFromRef(node.RootID)
		stat, ok := idx[rootRPID]
		if !ok {
			continue
		}
		childID := commentRPID(node)
		if childID == "" {
			continue
		}
		if _, exists := stat.ChildIDs[childID]; exists {
			continue
		}
		stat.ChildIDs[childID] = struct{}{}
		stat.LiveChildren++
		idx[rootRPID] = stat
	}
	return idx
}

func expandReasonFor(root bilibili.Reply, idx liveRootIndex, kind walkKind) expandReason {
	stat, known := idx[root.RPID]
	if kind.expandEveryPopulatedRoot() {
		if root.RCount > 0 {
			return expandBaseline
		}
		return expandNone
	}
	if !known {
		if root.RCount > 0 {
			return expandNewRoot
		}
		return expandNone
	}
	if int64(stat.LiveChildren) != root.RCount {
		return expandChildGrowth
	}
	for _, preview := range root.Preview {
		if preview.RPID == "" || preview.RPID == root.RPID {
			continue
		}
		if _, ok := stat.ChildIDs[preview.RPID]; !ok {
			return expandPreviewUnknown
		}
	}
	return expandNone
}

func commentWalkReady(walk commentWalk) error {
	if !walk.RootsExhaustive {
		return fmt.Errorf("%w: root census truncated", errCommentWalkIncomplete)
	}
	for rpid := range walk.Expand {
		if !walk.ChildExhaustive[rpid] {
			return fmt.Errorf("%w: child walk truncated", errCommentWalkIncomplete)
		}
	}
	return nil
}

func (e *Engine) walkBiliComments(ctx context.Context, target model.CommentTarget, idx liveRootIndex, kind walkKind) (fetchedComments, commentWalk, error) {
	fetched := fetchedComments{Roots: make(map[string]bilibili.Reply), Children: make(map[string]bilibili.Reply)}
	walk := commentWalk{Expand: make(map[string]expandReason), ChildExhaustive: make(map[string]bool)}
	roots, exhaustive, err := e.pageCommentReplies(ctx, func(ctx context.Context, pn int) (bilibili.ReplyPage, error) {
		return e.client.ListRootReplies(ctx, target.CommentType, target.CommentOID, pn, 20)
	})
	if err != nil {
		return fetchedComments{}, commentWalk{}, err
	}
	walk.RootsExhaustive = exhaustive
	for _, reply := range roots {
		fetched.Roots[reply.RPID] = reply
		if reason := expandReasonFor(reply, idx, kind); reason != expandNone {
			walk.Expand[reply.RPID] = reason
		}
	}
	for rootID := range walk.Expand {
		children, childExhaustive, err := e.pageCommentReplies(ctx, func(ctx context.Context, pn int) (bilibili.ReplyPage, error) {
			return e.client.ListChildReplies(ctx, target.CommentType, target.CommentOID, rootID, pn, 20)
		})
		if err != nil {
			return fetchedComments{}, commentWalk{}, err
		}
		walk.ChildExhaustive[rootID] = childExhaustive
		for _, reply := range children {
			if reply.Root == "" {
				reply.Root = rootID
			}
			fetched.Children[reply.RPID] = reply
		}
	}
	return fetched, walk, nil
}

func (e *Engine) pageCommentReplies(ctx context.Context, list func(context.Context, int) (bilibili.ReplyPage, error)) ([]bilibili.Reply, bool, error) {
	seen := make(map[string]bool)
	replies := make([]bilibili.Reply, 0)
	for pn := 1; pn <= 10000; pn++ {
		requestCtx, cancel := context.WithTimeout(ctx, e.httpTimeout)
		page, err := list(requestCtx, pn)
		cancel()
		if err != nil {
			return nil, false, err
		}
		signature := replyPageSignature(page.Replies)
		if page.HasMore && (signature == "" || seen[signature]) {
			return replies, false, nil
		}
		seen[signature] = true
		replies = append(replies, page.Replies...)
		if !page.HasMore {
			return replies, true, nil
		}
		if pn == 10000 {
			return replies, false, nil
		}
	}
	return replies, false, nil
}

func assembleCommentSnapshot(in assembleInput) commentSnapshot {
	complete := in.Walk.RootsExhaustive
	for rpid := range in.Walk.Expand {
		if !in.Walk.ChildExhaustive[rpid] {
			complete = false
			break
		}
	}
	byID := make(map[string]model.CommentNode)
	add := func(node model.CommentNode) {
		if node.ID == "" {
			node.ID = model.CommentID(model.PlatformBilibili, node.RPID)
		}
		if node.ID == "" {
			return
		}
		node.Children = nil
		byID[node.ID] = node
	}
	if in.Fetched.Roots == nil {
		in.Fetched.Roots = map[string]bilibili.Reply{}
	}
	if in.Fetched.Children == nil {
		in.Fetched.Children = map[string]bilibili.Reply{}
	}
	for _, reply := range in.Fetched.Roots {
		add(biliCommentNode(in.ContentID, in.UPUID, reply, true))
	}
	for _, reply := range in.Fetched.Children {
		add(biliCommentNode(in.ContentID, in.UPUID, reply, false))
	}
	for _, node := range flattenCommentNodes(in.Stored) {
		if !isLiveComment(node) {
			continue
		}
		rootRPID := commentRPID(node)
		if !isCommentRoot(node) {
			rootRPID = commentRPIDFromRef(node.RootID)
		}
		if _, inCensus := in.Fetched.Roots[rootRPID]; !inCensus {
			continue
		}
		if _, expanded := in.Walk.Expand[rootRPID]; expanded {
			continue
		}
		if _, exists := byID[node.ID]; exists {
			continue
		}
		add(node)
	}
	nodes := make([]model.CommentNode, 0, len(byID))
	for _, node := range byID {
		nodes = append(nodes, node)
	}
	slices.SortFunc(nodes, func(a, b model.CommentNode) int {
		if order := a.Time.Compare(b.Time); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return commentSnapshot{nodes: nodes, complete: complete}
}

func (e *Engine) ensureCommentContent(store *state.Store, target model.CommentTarget) (model.Content, error) {
	content, _, err := store.Content(model.ContentID(model.PlatformBilibili, target.DynamicID))
	if err == nil {
		return content, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return model.Content{}, err
	}
	externalID := target.DynamicID
	if externalID == "" {
		externalID = target.CommentOID
	}
	source := model.Source{
		ID: model.SourceID(model.PlatformBilibili, target.UID), Platform: model.PlatformBilibili,
		Type: model.SourceBilibiliUP, ExternalID: target.UID, Name: target.UPName, Enabled: true, BaselineState: model.BaselineComplete,
	}
	if err := store.PutSource(source); err != nil {
		return model.Content{}, err
	}
	content = model.Content{
		ID: model.ContentID(model.PlatformBilibili, externalID), Platform: model.PlatformBilibili,
		SourceID: source.ID, ExternalID: externalID, AuthorID: target.UID, AuthorName: target.UPName,
		UpstreamType: firstNonEmptyString(target.ContentType, "commentable"), Type: biliContentType(target.ContentType),
		Title: target.Title, URL: target.URL, PublishedAt: target.PublishedAt, LastSyncedAt: time.Now(),
	}
	if content.PublishedAt.IsZero() {
		content.PublishedAt = time.Now()
	}
	return content, nil
}

func loadStoredComments(store *state.Store, contentID string) ([]model.CommentNode, error) {
	tree, _, err := store.CommentTree(contentID)
	if errors.Is(err, state.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return flattenCommentNodes(tree), nil
}

func flattenCommentNodes(nodes []model.CommentNode) []model.CommentNode {
	out := make([]model.CommentNode, 0, len(nodes))
	var walk func(model.CommentNode)
	walk = func(node model.CommentNode) {
		children := node.Children
		node.Children = nil
		out = append(out, node)
		for _, child := range children {
			walk(child)
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	return out
}

func isLiveComment(node model.CommentNode) bool {
	return node.DeletedAt.IsZero()
}

func isCommentRoot(node model.CommentNode) bool {
	return node.ParentID == "" && node.Parent == ""
}

func commentRPID(node model.CommentNode) string {
	if node.RPID != "" {
		return node.RPID
	}
	return commentRPIDFromRef(node.ID)
}

func commentRPIDFromRef(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}
