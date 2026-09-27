// Package jevdom locates observed accessibility nodes with typed Jev decisions.
// It has no page evaluation, selector generation, or browser action surface.
package jevdom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/repplus/rep-cli/internal/browserrpc"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxFrames = 16
const maxNodes = 100000

type Browser = browserrpc.Caller

type Candidate struct {
	ID                 string            `json:"id"`
	Role               string            `json:"role"`
	Name               string            `json:"name"`
	Context            string            `json:"context,omitempty"`
	States             map[string]any    `json:"states,omitempty"`
	TextTruncated      bool              `json:"text_truncated,omitempty"`
	FrameID            string            `json:"frame_id"`
	BackendDOMNodeID   int64             `json:"backend_dom_node_id"`
	SessionID          string            `json:"session_id,omitempty"`
	DocumentGeneration string            `json:"document_generation,omitempty"`
	FrameURL           string            `json:"frame_url,omitempty"`
	ContextRelations   []ContextRelation `json:"context_relations,omitempty"`
}

type ContextRelation struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

type FrameIssue struct {
	FrameID string `json:"frame_id"`
	Reason  string `json:"reason"`
}

type Coverage struct {
	TotalCandidates   int          `json:"total_candidates"`
	Considered        int          `json:"considered"`
	Truncated         bool         `json:"truncated"`
	FramesRead        int          `json:"frames_read"`
	UnavailableFrames int          `json:"unavailable_frames"`
	OmittedNodes      int          `json:"omitted_nodes"`
	TextTruncated     int          `json:"text_truncated"`
	FrameIssues       []FrameIssue `json:"frame_issues,omitempty"`
}

type Snapshot struct {
	Schema      int         `json:"schema"`
	Generation  string      `json:"generation"`
	Candidates  []Candidate `json:"candidates"`
	Fingerprint string      `json:"fingerprint"`
	Coverage    Coverage    `json:"coverage"`
}

type frameTree struct {
	Frame struct {
		ID       string `json:"id"`
		LoaderID string `json:"loaderId"`
		URL      string `json:"url"`
	} `json:"frame"`
	Children []frameTree `json:"childFrames"`
}

type axValue struct {
	Value any `json:"value"`
}
type axNode struct {
	NodeID           string   `json:"nodeId"`
	ParentID         string   `json:"parentId"`
	ChildIDs         []string `json:"childIds"`
	BackendDOMNodeID int64    `json:"backendDOMNodeId"`
	FrameID          string   `json:"frameId"`
	Ignored          bool     `json:"ignored"`
	Role             axValue  `json:"role"`
	Name             axValue  `json:"name"`
	Properties       []struct {
		Name  string  `json:"name"`
		Value axValue `json:"value"`
	} `json:"properties"`
}

func (node axNode) role() string { value, _ := node.Role.Value.(string); return value }
func (node axNode) name() string { value, _ := node.Name.Value.(string); return value }
func (node axNode) blocked() bool {
	for _, property := range node.Properties {
		if property.Name == "disabled" || property.Name == "hidden" {
			if property.Value.Value == true || property.Value.Value == "true" {
				return true
			}
		}
	}
	return false
}

// documentRoot identifies a frame's document node. Its accessible name is the
// page title, shared by every candidate in the frame, so it never distinguishes
// one candidate from another.
func (node axNode) documentRoot() bool {
	role := strings.ToLower(node.role())
	return role == "rootwebarea" || role == "webarea"
}

func (node axNode) headingLevel() int {
	for _, property := range node.Properties {
		if property.Name != "level" {
			continue
		}
		switch value := property.Value.Value.(type) {
		case float64:
			if value >= 1 && value <= 6 && value == float64(int(value)) {
				return int(value)
			}
		case string:
			if level, err := strconv.Atoi(value); err == nil && level >= 1 && level <= 6 {
				return level
			}
		}
	}
	return 2 // the ARIA default for a heading without a level
}

func (node axNode) editableBoundary() bool {
	switch strings.ToLower(node.role()) {
	case "textbox", "searchbox", "combobox", "spinbutton":
		return true
	}
	for _, property := range node.Properties {
		if property.Name == "editable" || property.Name == "richlyEditable" {
			if property.Value.Value == true || property.Value.Value == "true" || property.Value.Value == "plaintext" || property.Value.Value == "richtext" {
				return true
			}
		}
	}
	return false
}

var safeStates = wordSet("checked selected expanded pressed required readonly invalid modal busy")
var safeEnums = wordSet("mixed true false grammar spelling")

func (node axNode) states() map[string]any {
	result := map[string]any{}
	for _, property := range node.Properties {
		if !safeStates[property.Name] {
			continue
		}
		switch value := property.Value.Value.(type) {
		case bool:
			result[property.Name] = value
		case string:
			if safeEnums[value] {
				result[property.Name] = value
			}
		}
	}
	return result
}

var controls = wordSet("button checkbox combobox link listbox menuitem menuitemcheckbox menuitemradio option radio scrollbar searchbox slider spinbutton switch tab textbox treeitem")
var texts = wordSet("statictext heading paragraph labeltext caption legend term definition")

func wordSet(value string) map[string]bool {
	result := map[string]bool{}
	for _, word := range strings.Fields(value) {
		result[word] = true
	}
	return result
}

// Capture uses only Page.getFrameTree and Accessibility.getFullAXTree. Node
// values, HTML, source attributes, URLs, and form values are never candidates.
func Capture(ctx context.Context, browser Browser, tabID int, kind, origin string) (Snapshot, error) {
	return captureOptions(ctx, browser, Options{TabID: tabID, Kind: kind, Origin: origin})
}

func captureOptions(ctx context.Context, browser Browser, options Options) (Snapshot, error) {
	tabID, kind, origin := options.TabID, options.Kind, options.Origin
	if browser == nil || tabID < 0 {
		return Snapshot{}, errors.New("a browser and nonnegative tab id are required")
	}
	var tree struct {
		FrameTree frameTree `json:"frameTree"`
	}
	if err := cdp(ctx, browser, tabID, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
		return Snapshot{}, errors.New("cannot read the page frame tree")
	}
	if tree.FrameTree.Frame.ID == "" {
		return Snapshot{}, errors.New("browser returned an invalid frame tree")
	}
	if origin != "" && pageOrigin(tree.FrameTree.Frame.URL) != origin {
		return Snapshot{}, errors.New("page origin does not match the requested origin")
	}
	frames := []frameTree{}
	var flatten func(frameTree, int) error
	flatten = func(frame frameTree, depth int) error {
		if depth > 64 || len(frames) >= 1000 || frame.Frame.ID == "" {
			return errors.New("page frame tree exceeds the supported bounds")
		}
		frames = append(frames, frame)
		for _, child := range frame.Children {
			if err := flatten(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := flatten(tree.FrameTree, 0); err != nil {
		return Snapshot{}, err
	}
	if options.FrameID != "" || options.FrameURL != "" {
		selected := []frameTree{}
		for _, frame := range frames {
			if (options.FrameID == "" || options.FrameID == frame.Frame.ID) && (options.FrameURL == "" || options.FrameURL == frame.Frame.URL) {
				selected = append(selected, frame)
			}
		}
		if len(selected) == 0 {
			return Snapshot{}, errors.New("requested frame is unavailable")
		}
		if len(selected) > 1 {
			return Snapshot{}, errors.New("requested frame URL is ambiguous; specify its frame id")
		}
		frames = selected
	}
	result := Snapshot{Schema: 1, Generation: frameIdentity(tree.FrameTree), Candidates: []Candidate{}}
	rootFrameID := tree.FrameTree.Frame.ID
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	_ = encoder.Encode([]any{"jev-dom-snapshot-v3", tabID, kind, origin, options.FrameID, options.FrameURL, options.ScopeBackendDOMNodeID})
	seen := map[string]bool{}
	for index, frame := range frames {
		// URLs contribute only a digest to the local fingerprint, never output.
		_ = encoder.Encode([]string{frame.Frame.ID, frame.Frame.LoaderID, digest(frame.Frame.URL)})
		unavailable := ""
		if index >= maxFrames {
			unavailable = "frame_limit"
		}
		if origin != "" && pageOrigin(frame.Frame.URL) != origin {
			unavailable = "outside_origin"
		}
		if unavailable != "" {
			result.Coverage.FrameIssues = append(result.Coverage.FrameIssues, FrameIssue{frame.Frame.ID, unavailable})
			_ = encoder.Encode(unavailable)
			continue
		}
		var tree struct {
			Nodes []axNode `json:"nodes"`
		}
		frameCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := cdp(frameCtx, browser, tabID, "Accessibility.getFullAXTree", map[string]any{"frameId": frame.Frame.ID}, &tree)
		cancel()
		if err != nil || len(tree.Nodes) == 0 || len(tree.Nodes) > maxNodes {
			if index == 0 {
				return Snapshot{}, errors.New("cannot read the top-frame accessibility tree")
			}
			result.Coverage.FrameIssues = append(result.Coverage.FrameIssues, FrameIssue{frame.Frame.ID, "accessibility_unavailable"})
			_ = encoder.Encode("accessibility_unavailable")
			continue
		}
		result.Coverage.FramesRead++
		_ = encoder.Encode("available")
		byID := make(map[string]axNode, len(tree.Nodes))
		children := make(map[string][]axNode, len(tree.Nodes))
		for _, node := range tree.Nodes {
			byID[node.NodeID] = node
			children[node.ParentID] = append(children[node.ParentID], node)
		}
		headings := sectionHeadings(tree.Nodes, byID, children)
		topFrame := frame.Frame.ID == rootFrameID
		var eligible map[string]bool
		if options.ScopeBackendDOMNodeID > 0 {
			var scopeRoot string
			for _, node := range tree.Nodes {
				if node.BackendDOMNodeID == options.ScopeBackendDOMNodeID {
					if scopeRoot != "" && scopeRoot != node.NodeID {
						return Snapshot{}, errors.New("subtree root is ambiguous")
					}
					scopeRoot = node.NodeID
				}
			}
			if scopeRoot == "" {
				return Snapshot{}, errors.New("subtree root is unavailable")
			}
			eligible = map[string]bool{}
			var walkScope func(string, int) error
			walkScope = func(id string, depth int) error {
				if depth > 64 || eligible[id] {
					return errors.New("subtree scope is cyclic or exceeds depth bounds")
				}
				eligible[id] = true
				for _, child := range children[id] {
					if err := walkScope(child.NodeID, depth+1); err != nil {
						return err
					}
				}
				return nil
			}
			if err := walkScope(scopeRoot, 0); err != nil {
				return Snapshot{}, err
			}
		}
		for _, node := range tree.Nodes {
			if eligible != nil && !eligible[node.NodeID] {
				continue
			}
			role := strings.ToLower(node.role())
			allowed := (kind == "controls" || kind == "all") && controls[role] || (kind == "text" || kind == "all") && texts[role]
			if node.Ignored || node.BackendDOMNodeID <= 0 || !allowed || strings.TrimSpace(node.name()) == "" || (!controls[role] && node.editableBoundary()) {
				continue
			}
			if node.FrameID != "" && node.FrameID != frame.Frame.ID {
				continue
			}
			blocked, contextName := node.blocked(), ""
			parentID := node.ParentID
			visited := map[string]bool{node.NodeID: true}
			for depth := 0; depth < 64 && parentID != ""; depth++ {
				parent, exists := byID[parentID]
				if !exists || visited[parent.NodeID] {
					break
				}
				visited[parent.NodeID] = true
				blocked = blocked || parent.blocked()
				// Editable field values also appear as StaticText descendants in
				// Chromium's AX tree. Do not treat those names as page labels.
				if parent.editableBoundary() {
					if strings.ToLower(parent.role()) == "combobox" && (role == "option" || strings.HasPrefix(role, "menuitem")) {
						parentID = parent.ParentID
						continue
					}
					blocked, parentID = true, ""
					break
				}
				if contextName == "" && !parent.Ignored && parent.name() != "" && parent.name() != node.name() && !(topFrame && parent.documentRoot()) {
					contextName = parent.name()
				}
				parentID = parent.ParentID
			}
			if parentID != "" {
				result.Coverage.OmittedNodes++
				continue
			}
			if blocked {
				continue
			}
			id := "c_" + digest(fmt.Sprintf("%s:%d", frame.Frame.ID, node.BackendDOMNodeID))[:16]
			if seen[id] {
				continue
			}
			seen[id] = true
			// Hash all candidate names before redaction/text truncation, so changes
			// outside a model-visible prefix still invalidate a reused handle.
			relations, relationOmitted := relationContext(node, byID, children)
			relations = withSectionHeading(relations, headings[node.NodeID], node.name(), contextName)
			result.Coverage.OmittedNodes += relationOmitted
			_ = encoder.Encode([]any{id, role, node.name(), contextName, node.states(), relations})
			fullName, fullContext := scrub(node.name(), len(node.name())*4+128), scrub(contextName, len(contextName)*4+128)
			truncated := len(fullName) > 180 || len(fullContext) > 120 || len(relations) > 8
			relations = relations[:min(len(relations), 8)]
			for index := range relations {
				full := scrub(relations[index].Name, len(relations[index].Name)*4+128)
				truncated = truncated || len(full) > 240
				relations[index].Name = scrub(full, 240)
			}
			if truncated {
				result.Coverage.TextTruncated++
			}
			result.Candidates = append(result.Candidates, Candidate{ID: id, Role: scrub(role, 48),
				Name: scrub(fullName, 180), Context: scrub(fullContext, 120), States: node.states(), TextTruncated: truncated,
				FrameID: frame.Frame.ID, BackendDOMNodeID: node.BackendDOMNodeID,
				DocumentGeneration: frame.Frame.LoaderID, FrameURL: frame.Frame.URL, ContextRelations: relations})
		}
	}
	var verified struct {
		FrameTree frameTree `json:"frameTree"`
	}
	if cdp(ctx, browser, tabID, "Page.getFrameTree", map[string]any{}, &verified) != nil || frameIdentity(tree.FrameTree) != frameIdentity(verified.FrameTree) {
		return Snapshot{}, errors.New("page changed while its accessibility snapshot was read")
	}
	_ = encoder.Encode(result.Coverage.OmittedNodes)
	result.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	result.Coverage.TotalCandidates = len(result.Candidates)
	result.Coverage.UnavailableFrames = len(result.Coverage.FrameIssues)
	return result, nil
}

func frameIdentity(tree frameTree) string {
	// Keep the frame identity, loader, URL, and descendants coherent across AX
	// reads; this digest and all source URLs remain inside the local process.
	data, _ := json.Marshal(tree)
	return digest(string(data))
}

func cdp(ctx context.Context, browser Browser, tabID int, method string, params map[string]any, out any) error {
	if err := browserrpc.CDP(ctx, browser, tabID, method, params, out); err != nil {
		return errors.New("browser accessibility read failed")
	}
	return nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

var urlPattern = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>]+`)
var emailPattern = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[^\s@]+`)
var credentialPattern = regexp.MustCompile(`(?i)\b(?:bearer\s+\S+|(?:api[_ -]?key|access[_ -]?token|password|secret)\s*[:=]\s*\S+)`)

func scrub(value string, limit int) string {
	value = urlPattern.ReplaceAllString(value, "[link omitted]")
	value = emailPattern.ReplaceAllString(value, "[email omitted]")
	value = credentialPattern.ReplaceAllString(value, "[credential omitted]")
	value = strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func pageOrigin(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return ""
	}
	host := strings.ToLower(parsed.Host)
	if parsed.Scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	}
	if parsed.Scheme == "http" {
		host = strings.TrimSuffix(host, ":80")
	}
	return parsed.Scheme + "://" + host
}

func validateOrigin(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || pageOrigin(value) == "" {
		return "", errors.New("origin must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	return pageOrigin(value), nil
}

func shortlist(candidates []Candidate, goal string, limit int) []Candidate {
	if len(candidates) <= limit {
		return append([]Candidate(nil), candidates...)
	}
	terms := tokens(goal)
	type ranked struct {
		candidate Candidate
		score     int
	}
	ranking := make([]ranked, len(candidates))
	for i, candidate := range candidates {
		name, other := tokens(candidate.Name), tokens(candidate.Role+" "+candidate.Context)
		score := 0
		for term := range terms {
			if name[term] {
				score += 3
			}
			if other[term] {
				score++
			}
		}
		ranking[i] = ranked{candidate, score}
	}
	sort.SliceStable(ranking, func(i, j int) bool { return ranking[i].score > ranking[j].score })
	result := make([]Candidate, limit)
	for index := range result {
		result[index] = ranking[index].candidate
	}
	return result
}

func tokens(value string) map[string]bool {
	result := map[string]bool{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len(token) > 1 {
			result[token] = true
		}
	}
	return result
}
