package cmd

import "strings"

// Vendored from grove's session_tree.go. Builds an ASCII tree of session names
// grouped by "/" segments. Each row exposes its tree-drawing prefix (branch)
// and node segment separately so the tree picker can color them independently,
// plus the switch target (defaultTarget) for non-leaf rows.

type sessionTreeNode struct {
	segment       string
	sessionName   string
	defaultTarget string
	leafCount     int
	children      []*sessionTreeNode
	childIndex    map[string]*sessionTreeNode
}

type sessionTreeRow struct {
	branch        string // tree-drawing prefix, e.g. "│   └── "
	segment       string // the node's own name segment
	depth         int
	hasChild      bool
	sessionName   string
	defaultTarget string
	leafCount     int
}

// label is the full visible text (branch + segment), used by tests and as the
// plain rendering fallback.
func (r sessionTreeRow) label() string { return r.branch + r.segment }

func buildSessionTreeRows(sessionNames []string) []sessionTreeRow {
	root := &sessionTreeNode{}
	for _, sessionName := range sessionNames {
		node := root
		seen := []*sessionTreeNode{root}
		for _, segment := range treeSegments(sessionName) {
			node = node.child(segment)
			seen = append(seen, node)
		}
		node.sessionName = sessionName
		for _, seenNode := range seen {
			if seenNode.defaultTarget == "" {
				seenNode.defaultTarget = sessionName
			}
			seenNode.leafCount++
		}
	}

	var rows []sessionTreeRow
	for i, child := range root.children {
		appendSessionTreeRows(&rows, child, "", 0, i == len(root.children)-1)
	}
	return rows
}

func treeSegments(sessionName string) []string {
	parts := strings.Split(sessionName, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		segments = append(segments, part)
	}
	if len(segments) > 1 && segments[0] == "g" {
		return segments[1:]
	}
	return segments
}

func (n *sessionTreeNode) child(segment string) *sessionTreeNode {
	if n.childIndex == nil {
		n.childIndex = make(map[string]*sessionTreeNode)
	}
	if child, ok := n.childIndex[segment]; ok {
		return child
	}
	child := &sessionTreeNode{segment: segment}
	n.childIndex[segment] = child
	n.children = append(n.children, child)
	return child
}

func appendSessionTreeRows(rows *[]sessionTreeRow, node *sessionTreeNode, prefix string, depth int, isLast bool) {
	connector := "├── "
	childPrefix := prefix + "│   "
	if isLast {
		connector = "└── "
		childPrefix = prefix + "    "
	}

	*rows = append(*rows, sessionTreeRow{
		branch:        prefix + connector,
		segment:       node.segment,
		depth:         depth,
		hasChild:      len(node.children) > 0,
		sessionName:   node.sessionName,
		defaultTarget: node.defaultTarget,
		leafCount:     node.leafCount,
	})

	for i, child := range node.children {
		appendSessionTreeRows(rows, child, childPrefix, depth+1, i == len(node.children)-1)
	}
}
