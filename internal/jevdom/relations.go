package jevdom

import "strings"

var entityRoles = wordSet("row layouttablerow listitem article")
var relationRoles = wordSet("row layouttablerow listitem article columnheader rowheader heading form dialog table layouttable grid group region list navigation main banner complementary")

// relationContext keeps labels associated with the entity containing a control.
// It deliberately never copies aggregate cell/container names: Chromium may
// derive those names from editable values. Only noneditable text is collected.
func relationContext(node axNode, byID map[string]axNode, children map[string][]axNode) ([]ContextRelation, int) {
	var result []ContextRelation
	seen := map[string]bool{node.NodeID: true}
	omitted := 0
	entityRead := false
	parentID := node.ParentID
	for depth := 0; parentID != "" && depth < 64; depth++ {
		parent, exists := byID[parentID]
		if !exists || seen[parentID] {
			break
		}
		seen[parentID] = true
		role := strings.ToLower(parent.role())
		name := ""
		if entityRoles[role] && !entityRead {
			entityRead = true
			var lost int
			name, lost = entityText(parent, children)
			omitted += lost
		}
		if strings.TrimSpace(name) != "" && name != node.name() {
			result = append(result, ContextRelation{Role: role, Name: name})
		}
		if relationRoles[role] && !parent.Ignored && !parent.editableBoundary() && parent.name() != "" && parent.name() != node.name() {
			relation := ContextRelation{Role: role, Name: parent.name()}
			exists := false
			for _, prior := range result {
				if prior == relation {
					exists = true
				}
			}
			if !exists {
				result = append(result, relation)
			}
		}
		parentID = parent.ParentID
	}
	return result, omitted
}

func entityText(root axNode, children map[string][]axNode) (string, int) {
	var parts []string
	seenText, visited := map[string]bool{}, map[string]bool{}
	count, omitted := 0, 0
	var walk func(axNode, int)
	walk = func(node axNode, depth int) {
		if count >= 2048 || depth > 64 {
			omitted++
			return
		}
		if visited[node.NodeID] {
			omitted++
			return
		}
		visited[node.NodeID] = true
		count++
		role := strings.ToLower(node.role())
		if node.blocked() || node.editableBoundary() || (controls[role] && role != "link") {
			return
		}
		name := strings.TrimSpace(node.name())
		if !node.Ignored && (texts[role] || role == "link") && name != "" && !seenText[name] {
			seenText[name] = true
			parts = append(parts, name)
		}
		if role == "link" {
			return
		}
		for _, child := range children[node.NodeID] {
			walk(child, depth+1)
		}
	}
	for _, child := range children[root.NodeID] {
		walk(child, 1)
	}
	return strings.Join(parts, " | "), omitted
}

// sectionHeadings maps each node to the nearest preceding heading in document
// order: the label a reader sees for the section that contains it. A heading
// maps to the nearest preceding heading of a higher level. Child order follows
// childIds, falling back to response order.
func sectionHeadings(nodes []axNode, byID map[string]axNode, children map[string][]axNode) map[string]string {
	result := make(map[string]string, len(nodes))
	var stack [7]string
	nearest := func(below int) string {
		for level := below - 1; level >= 1; level-- {
			if stack[level] != "" {
				return stack[level]
			}
		}
		return ""
	}
	type item struct {
		id    string
		depth int
	}
	var pending []item
	for index := len(nodes) - 1; index >= 0; index-- {
		if _, parent := byID[nodes[index].ParentID]; nodes[index].ParentID == "" || !parent {
			pending = append(pending, item{nodes[index].NodeID, 0})
		}
	}
	visited := make(map[string]bool, len(nodes))
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		node, exists := byID[current.id]
		if !exists || visited[current.id] || current.depth > 256 {
			continue
		}
		visited[current.id] = true
		name := strings.TrimSpace(node.name())
		heading := strings.EqualFold(node.role(), "heading") && !node.Ignored && !node.blocked() && name != ""
		level := len(stack)
		if heading {
			level = node.headingLevel()
		}
		result[node.NodeID] = nearest(level)
		if heading {
			stack[level] = name
			for deeper := level + 1; deeper < len(stack); deeper++ {
				stack[deeper] = ""
			}
		}
		next := node.ChildIDs
		if len(next) == 0 {
			for _, child := range children[node.NodeID] {
				next = append(next, child.NodeID)
			}
		}
		for index := len(next) - 1; index >= 0; index-- {
			pending = append(pending, item{next[index], current.depth + 1})
		}
	}
	return result
}

// withSectionHeading adds the section heading as a relation when it tells the
// model something new. It never displaces entity or ancestor relations.
func withSectionHeading(relations []ContextRelation, heading, name, context string) []ContextRelation {
	heading = strings.TrimSpace(heading)
	if heading == "" || heading == strings.TrimSpace(name) || heading == context || len(relations) >= 8 {
		return relations
	}
	for _, relation := range relations {
		if relation.Name == heading {
			return relations
		}
	}
	return append(relations, ContextRelation{Role: "heading", Name: heading})
}
