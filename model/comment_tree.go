package model

import (
	"cmp"
	"slices"
)

// BuildCommentTree converts an adjacency list into a deterministic nested tree.
// Orphans are kept as roots so archived data remains visible. A cycle is broken
// at the first node (in stable time/id order) that would revisit an ancestor.
// incomplete reports either condition to callers so they can avoid inferring
// deletions or presenting an apparently complete upstream tree.
func BuildCommentTree(nodes []CommentNode) (roots []CommentNode, incomplete bool) {
	items := make(map[string]CommentNode, len(nodes))
	order := make([]string, 0, len(nodes))
	for _, input := range nodes {
		node := input
		if node.ID == "" {
			node.ID = node.RPID
		}
		if node.ID == "" {
			incomplete = true
			continue
		}
		node.Children = nil
		if _, exists := items[node.ID]; !exists {
			order = append(order, node.ID)
		}
		items[node.ID] = node
	}
	slices.SortFunc(order, func(a, b string) int {
		left, right := items[a], items[b]
		if byTime := left.Time.Compare(right.Time); byTime != 0 {
			return byTime
		}
		return cmp.Compare(a, b)
	})

	children := make(map[string][]string, len(items))
	rootIDs := make([]string, 0)
	for _, id := range order {
		node := items[id]
		parent := node.ParentID
		if parent == "" {
			parent = node.Parent
		}
		if parent == "" {
			rootIDs = append(rootIDs, id)
			continue
		}
		if parent == id || createsCycle(id, parent, items) {
			incomplete = true
			rootIDs = append(rootIDs, id)
			continue
		}
		if _, exists := items[parent]; !exists {
			incomplete = true
			rootIDs = append(rootIDs, id)
			continue
		}
		children[parent] = append(children[parent], id)
	}

	var build func(string, map[string]bool) CommentNode
	build = func(id string, visiting map[string]bool) CommentNode {
		node := items[id]
		visiting[id] = true
		for _, childID := range children[id] {
			if visiting[childID] {
				incomplete = true
				continue
			}
			node.Children = append(node.Children, build(childID, visiting))
		}
		delete(visiting, id)
		return node
	}
	seen := make(map[string]bool, len(items))
	for _, id := range rootIDs {
		root := build(id, make(map[string]bool))
		roots = append(roots, root)
		markTreeSeen(root, seen)
	}
	// Defensive visibility for any component not reachable after malformed links.
	for _, id := range order {
		if !seen[id] {
			incomplete = true
			root := build(id, make(map[string]bool))
			roots = append(roots, root)
			markTreeSeen(root, seen)
		}
	}
	return roots, incomplete
}

func createsCycle(id, parent string, nodes map[string]CommentNode) bool {
	visited := map[string]bool{id: true}
	for parent != "" {
		if visited[parent] {
			return true
		}
		visited[parent] = true
		node, ok := nodes[parent]
		if !ok {
			return false
		}
		parent = node.ParentID
		if parent == "" {
			parent = node.Parent
		}
	}
	return false
}

func markTreeSeen(node CommentNode, seen map[string]bool) {
	seen[node.ID] = true
	for _, child := range node.Children {
		markTreeSeen(child, seen)
	}
}

// CommentAncestorPath returns the trigger's path from the highest known root.
func CommentAncestorPath(nodes []CommentNode, triggerID string) (path []CommentNode, complete bool) {
	byID := make(map[string]CommentNode, len(nodes))
	for _, node := range nodes {
		if node.ID == "" {
			node.ID = node.RPID
		}
		byID[node.ID] = node
	}
	current, ok := byID[triggerID]
	if !ok {
		return nil, false
	}
	visited := make(map[string]bool)
	for {
		if visited[current.ID] {
			return nil, false
		}
		visited[current.ID] = true
		path = append(path, current)
		parent := current.ParentID
		if parent == "" {
			parent = current.Parent
		}
		if parent == "" {
			break
		}
		var exists bool
		current, exists = byID[parent]
		if !exists {
			slices.Reverse(path)
			return path, false
		}
	}
	slices.Reverse(path)
	return path, true
}
