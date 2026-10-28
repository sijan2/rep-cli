package cmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

type referenceArchive struct {
	Version    int                `json:"version"`
	ID         string             `json:"id"`
	Workspace  string             `json:"workspace"`
	Task       string             `json:"task"`
	Browser    string             `json:"browser"`
	Tab        int                `json:"tab_id"`
	Snapshot   browserfast.Result `json:"snapshot"`
	Screenshot map[string]any     `json:"screenshot"`
}

func referencePath(id string) (string, error) {
	if !validFastToken(id, "r_") {
		return "", errors.New("invalid reference snapshot ID")
	}
	selected, err := scope.Current()
	if err != nil || !selected.Scoped {
		return "", errors.New("reference snapshots require an explicit workspace and task")
	}
	return filepath.Join(selected.DataDir, "references", id), nil
}

func referenceClip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func referenceArray(value any) []any {
	items, _ := value.([]any)
	return items
}

func referenceMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func referenceTypography(elements, styles []any) ([]any, []any) {
	var body, heading map[string]any
	bodyIndex, headingIndex := -1, -1
	for index, row := range elements {
		e := referenceMap(row)
		tag, _ := e["tag"].(string)
		if tag == "body" {
			body, bodyIndex = e, index
		}
		if heading == nil && len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
			heading, headingIndex = e, index
		}
	}
	typography, rules := []any{}, []any{}
	for _, role := range []string{"body", "primary_headline"} {
		e := body
		index := bodyIndex
		if role == "primary_headline" {
			e = heading
			index = headingIndex
		}
		if e == nil {
			continue
		}
		css := referenceMap(e["computed"])
		typography = append(typography, map[string]any{"role": role, "element_id": e["id"], "selector": e["selector"], "text": e["text"], "json_pointer": fmt.Sprintf("/snapshot/reference/elements/%d/computed", index),
			"font_family": css["font-family"], "font_size": css["font-size"], "line_height": css["line-height"], "font_weight": css["font-weight"], "letter_spacing": css["letter-spacing"], "margin": css["margin"], "padding": css["padding"]})
	}
	if heading != nil {
		for index, row := range styles {
			r := referenceMap(row)
			declarations, _ := r["declarations"].(string)
			if !strings.Contains(declarations, "font-size:") {
				continue
			}
			for _, id := range referenceArray(r["elements"]) {
				if id == heading["id"] {
					projected := map[string]any{}
					for key, value := range r {
						projected[key] = value
					}
					projected["json_pointer"] = fmt.Sprintf("/snapshot/reference/styles/%d", index)
					rules = append(rules, projected)
					break
				}
			}
			if len(rules) >= 4 {
				break
			}
		}
	}
	return typography, rules
}

func referenceSections(data map[string]any) map[string][]any {
	sections := map[string][]any{}
	for _, key := range []string{"elements", "styles", "assets"} {
		sections[key] = referenceArray(data[key])
	}
	content := referenceMap(data["content"])
	for _, key := range []string{"headings", "links", "frames"} {
		sections[key] = referenceArray(content[key])
	}
	for _, key := range []string{"computed_stacks", "faces", "loaded_faces"} {
		for index, row := range referenceArray(referenceMap(data["fonts"])[key]) {
			sections["fonts"] = append(sections["fonts"], map[string]any{"kind": key, "data": row, "json_pointer": fmt.Sprintf("/snapshot/reference/fonts/%s/%d", key, index)})
		}
	}
	layout := referenceMap(data["layout"])
	sections["layout"] = []any{map[string]any{"kind": "viewport", "data": layout["viewport"], "json_pointer": "/snapshot/reference/layout/viewport"}}
	for _, key := range []string{"media_queries", "variables", "css_math", "animations", "stylesheets"} {
		for index, row := range referenceArray(layout[key]) {
			sections["layout"] = append(sections["layout"], map[string]any{"kind": key, "data": row, "json_pointer": fmt.Sprintf("/snapshot/reference/layout/%s/%d", key, index)})
		}
	}
	sections["performance"] = []any{data["performance"]}
	sections["css_math"] = referenceArray(layout["css_math"])
	return sections
}

func referenceReadNext(command *cobra.Command, id, section string, offset int, flags fastFlags) string {
	selected, _ := scope.Current()
	extra := ""
	if offset != 0 {
		extra += fmt.Sprintf(" --offset %d", offset)
	}
	if flags.maxText != 4096 {
		extra += fmt.Sprintf(" --max-text %d", flags.maxText)
	}
	if flags.limit != 40 {
		extra += fmt.Sprintf(" --limit %d", flags.limit)
	}
	return fmt.Sprintf("rep --workspace %s --task %s browser reference --saved %s --section %s%s", selected.Workspace, selected.Task, id, section, extra)
}

func referenceResume(command *cobra.Command, tab int, selector string, screenshot bool) string {
	extra := " --timeout 10s"
	maxText, _ := command.Flags().GetInt("max-text")
	limit, _ := command.Flags().GetInt("limit")
	if maxText != 4096 || limit != 40 {
		extra += fmt.Sprintf(" --max-text %d --limit %d", maxText, limit)
	}
	if keep, _ := command.Flags().GetBool("keep-tab"); keep {
		extra += " --keep-tab"
	}
	if selector != "" {
		extra += " --selector " + shellQuote(selector)
	}
	if !screenshot {
		extra += " --screenshot=false"
	}
	return fastNext(command, "reference", tab, extra)
}

func referenceFile(path string, data []byte) (map[string]any, error) {
	if err := writePrivateFile(path, data); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	return map[string]any{"path": path, "bytes": len(data), "sha256": hex.EncodeToString(digest[:])}, nil
}

// Snapshot reads never contact a browser and never substitute a fresh document.
func loadReference(id string) (referenceArchive, string, error) {
	var archive referenceArchive
	dir, err := referencePath(id)
	if err != nil {
		return archive, "", err
	}
	file, err := os.Open(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return archive, dir, fmt.Errorf("reference snapshot unavailable in this task: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 8*1024*1024 {
		return archive, dir, errors.New("invalid reference snapshot size")
	}
	if err := json.NewDecoder(file).Decode(&archive); err != nil {
		return archive, dir, err
	}
	selected, _ := scope.Current()
	if archive.Version != 1 || archive.ID != id || archive.Workspace != selected.Workspace || archive.Task != selected.Task {
		return archive, dir, errors.New("reference snapshot scope mismatch")
	}
	return archive, dir, nil
}

const referencePreviewBudgetBytes = 8 * 1024

type referencePreviewItem struct {
	value     any
	truncated bool
}

type referencePreviewGroup struct {
	name  string
	total int
	items []referencePreviewItem
}

func referencePreviewScalar(value any, stringLimit int) (any, bool) {
	switch typed := value.(type) {
	case nil, bool, float64:
		return typed, false
	case string:
		clipped := referenceClip(typed, stringLimit)
		return clipped, len(clipped) != len(typed)
	default:
		return nil, value != nil
	}
}

func referencePreviewScalars(value any, keys []string, stringLimit int) (map[string]any, bool) {
	source := referenceMap(value)
	result := map[string]any{}
	truncated := false
	for _, key := range keys {
		raw, ok := source[key]
		if !ok {
			continue
		}
		projected, clipped := referencePreviewScalar(raw, stringLimit)
		if projected != nil || raw == nil {
			result[key] = projected
		}
		truncated = truncated || clipped
	}
	return result, truncated
}

func referencePreviewStrings(value any, limit, stringLimit int) ([]any, int) {
	source := referenceArray(value)
	result := []any{}
	truncated := 0
	for _, row := range source[:min(limit, len(source))] {
		projected, clipped := referencePreviewScalar(row, stringLimit)
		if projected == nil {
			truncated++
			continue
		}
		result = append(result, projected)
		if clipped {
			truncated++
		}
	}
	return result, truncated
}

func referencePreviewGroups(data map[string]any, sections map[string][]any) []referencePreviewGroup {
	layout := referenceMap(data["layout"])
	fonts := referenceMap(data["fonts"])
	groups := []referencePreviewGroup{}

	typography, headlineCSS := referenceTypography(sections["elements"], sections["styles"])
	typographyItems := []referencePreviewItem{}
	for _, row := range typography {
		projected, clipped := referencePreviewScalars(row, []string{"role", "element_id", "selector", "text", "json_pointer", "font_family", "font_size", "line_height", "font_weight", "letter_spacing", "margin", "padding"}, 240)
		typographyItems = append(typographyItems, referencePreviewItem{value: projected, truncated: clipped})
	}
	groups = append(groups, referencePreviewGroup{name: "typography", total: len(typography), items: typographyItems})
	headlineItems := []referencePreviewItem{}
	for _, row := range headlineCSS {
		rule := referenceMap(row)
		projected, clipped := referencePreviewScalars(rule, []string{"source", "selector", "active", "json_pointer"}, 240)
		declarations, declarationsClipped := referencePreviewScalar(rule["declarations"], 600)
		projected["declarations"] = declarations
		conditions := []any{}
		for _, condition := range referenceArray(rule["conditions"])[:min(3, len(referenceArray(rule["conditions"])))] {
			item, limited := referencePreviewScalars(condition, []string{"kind", "text", "active"}, 240)
			conditions = append(conditions, item)
			clipped = clipped || limited
		}
		projected["conditions"] = conditions
		clipped = clipped || declarationsClipped || len(conditions) < len(referenceArray(rule["conditions"]))
		projected["preview_truncated"] = clipped
		headlineItems = append(headlineItems, referencePreviewItem{value: projected, truncated: clipped})
	}
	groups = append(groups, referencePreviewGroup{name: "headline_css", total: len(headlineCSS), items: headlineItems})

	fontRows := referenceArray(fonts["computed_stacks"])
	fontItems := []referencePreviewItem{}
	for _, row := range fontRows[:min(3, len(fontRows))] {
		value, clipped := referencePreviewScalar(row, 320)
		if value != nil {
			fontItems = append(fontItems, referencePreviewItem{value: value, truncated: clipped})
		}
	}
	groups = append(groups, referencePreviewGroup{name: "font_stacks", total: len(fontRows), items: fontItems})

	mediaRows := referenceArray(layout["media_queries"])
	mediaItems := []referencePreviewItem{}
	for _, row := range mediaRows[:min(3, len(mediaRows))] {
		item, clipped := referencePreviewScalars(row, []string{"text", "active"}, 320)
		mediaItems = append(mediaItems, referencePreviewItem{value: item, truncated: clipped})
	}
	groups = append(groups, referencePreviewGroup{name: "media_queries", total: len(mediaRows), items: mediaItems})

	mathRows := referenceArray(layout["css_math"])
	mathItems := []referencePreviewItem{}
	mathIndexes := []int{}
	for index, row := range mathRows {
		declarations, _ := referenceMap(row)["declarations"].(string)
		for _, declaration := range strings.Split(declarations, ";") {
			declaration = strings.TrimSpace(declaration)
			if !strings.HasPrefix(declaration, "--") && (strings.Contains(declaration, "calc(") || strings.Contains(declaration, "min(") || strings.Contains(declaration, "max(") || strings.Contains(declaration, "clamp(")) {
				mathIndexes = append(mathIndexes, index)
				break
			}
		}
	}
	for index := range mathRows {
		if len(mathIndexes) >= 3 {
			break
		}
		already := false
		for _, chosen := range mathIndexes {
			already = already || chosen == index
		}
		if !already {
			mathIndexes = append(mathIndexes, index)
		}
	}
	for _, index := range mathIndexes[:min(3, len(mathIndexes))] {
		row := mathRows[index]
		item := referenceMap(row)
		projected, clipped := referencePreviewScalars(item, []string{"source", "selector", "active"}, 240)
		projected["json_pointer"] = fmt.Sprintf("/snapshot/reference/layout/css_math/%d", index)
		declarations, declarationsClipped := referencePreviewScalar(item["declarations"], 600)
		projected["declarations"] = declarations
		elements, elementsClipped := referencePreviewStrings(item["elements"], 4, 80)
		projected["elements"] = elements
		conditions := []any{}
		for _, condition := range referenceArray(item["conditions"])[:min(3, len(referenceArray(item["conditions"])))] {
			value, limited := referencePreviewScalars(condition, []string{"kind", "text", "active"}, 160)
			conditions = append(conditions, value)
			clipped = clipped || limited
		}
		projected["conditions"] = conditions
		clipped = clipped || len(conditions) < len(referenceArray(item["conditions"]))
		for elementIndex, element := range sections["elements"] {
			e := referenceMap(element)
			matches := false
			for _, id := range referenceArray(item["elements"]) {
				matches = matches || id == e["id"]
			}
			if matches {
				measured, limited := referencePreviewScalars(e, []string{"id", "selector"}, 160)
				measured["json_pointer"] = fmt.Sprintf("/snapshot/reference/elements/%d", elementIndex)
				for name, source := range map[string]any{"computed_width": referenceMap(e["computed"])["width"], "css_pixel_width": referenceMap(e["rect"])["width"], "device_pixel_width": referenceMap(e["geometry"])["device_pixel_width"]} {
					value, valueClipped := referencePreviewScalar(source, 80)
					measured[name] = value
					limited = limited || valueClipped
				}
				projected["element_example"] = measured
				clipped = clipped || limited
				break
			}
		}
		if omitted := len(referenceArray(item["elements"])) - len(elements); omitted > 0 {
			projected["omitted_elements"] = omitted
			elementsClipped++
		}
		truncated := clipped || declarationsClipped || elementsClipped > 0
		projected["preview_truncated"] = truncated
		mathItems = append(mathItems, referencePreviewItem{value: projected, truncated: truncated})
	}
	groups = append(groups, referencePreviewGroup{name: "css_math", total: len(mathRows), items: mathItems})

	assetRows := sections["assets"]
	assetItems := []referencePreviewItem{}
	for _, row := range assetRows[:min(4, len(assetRows))] {
		item := referenceMap(row)
		projected, clipped := referencePreviewScalars(item, []string{"kind", "url", "natural_width", "natural_height", "rendered_width", "rendered_height"}, 400)
		projected["picture_sources"] = len(referenceArray(item["picture_sources"]))
		if clipped {
			projected["preview_truncated"] = true
		}
		assetItems = append(assetItems, referencePreviewItem{value: projected, truncated: clipped})
	}
	groups = append(groups, referencePreviewGroup{name: "assets", total: len(assetRows), items: assetItems})

	headingRows := sections["headings"]
	headingItems := []referencePreviewItem{}
	for _, row := range headingRows[:min(4, len(headingRows))] {
		projected, clipped := referencePreviewScalars(row, []string{"level", "text"}, 240)
		if clipped {
			projected["preview_truncated"] = true
		}
		headingItems = append(headingItems, referencePreviewItem{value: projected, truncated: clipped})
	}
	groups = append(groups, referencePreviewGroup{name: "headings", total: len(headingRows), items: headingItems})

	elementRows := sections["elements"]
	elementItems := []referencePreviewItem{}
	for _, row := range elementRows[:min(4, len(elementRows))] {
		element := referenceMap(row)
		projected, clipped := referencePreviewScalars(element, []string{"selector", "text"}, 240)
		rect, rectClipped := referencePreviewScalars(element["rect"], []string{"x", "y", "width", "height", "document_x", "document_y"}, 80)
		projected["rect"] = rect
		computed, computedClipped := referencePreviewScalars(referenceMap(element["computed"]), []string{"font-family", "font-size", "line-height", "display", "gap", "padding"}, 160)
		projected["font_family"] = computed["font-family"]
		projected["font_size"] = computed["font-size"]
		projected["line_height"] = computed["line-height"]
		projected["display"] = computed["display"]
		projected["gap"] = computed["gap"]
		projected["padding"] = computed["padding"]
		truncated := clipped || rectClipped || computedClipped
		if truncated {
			projected["preview_truncated"] = true
		}
		elementItems = append(elementItems, referencePreviewItem{value: projected, truncated: truncated})
	}
	groups = append(groups, referencePreviewGroup{name: "element_examples", total: len(elementRows), items: elementItems})
	return groups
}

func referencePreviewEncodedSize(preview map[string]any) int {
	coverage := referenceMap(preview["preview_coverage"])
	encoded, _ := json.Marshal(preview)
	for range 8 {
		if current, ok := coverage["encoded_bytes"].(int); ok && current == len(encoded) {
			return len(encoded)
		}
		coverage["encoded_bytes"] = len(encoded)
		encoded, _ = json.Marshal(preview)
	}
	return len(encoded)
}

// Keep essential facts at the front when an agent's tool truncates the output.
// This changes field order, not the preview schema or its hard byte budget.
func referenceOrderedPreview(preview map[string]any) json.RawMessage {
	encoded, _ := json.Marshal(struct {
		Viewport         any `json:"viewport,omitempty"`
		Paint            any `json:"paint,omitempty"`
		PerformanceScope any `json:"performance_scope"`
		Typography       any `json:"typography,omitempty"`
		HeadlineCSS      any `json:"headline_css,omitempty"`
		CSSMath          any `json:"css_math,omitempty"`
		Assets           any `json:"assets,omitempty"`
		FontStacks       any `json:"font_stacks,omitempty"`
		Headings         any `json:"headings,omitempty"`
		Navigation       any `json:"navigation,omitempty"`
		MediaQueries     any `json:"media_queries,omitempty"`
		ElementExamples  any `json:"element_examples,omitempty"`
		Coverage         any `json:"preview_coverage"`
	}{preview["viewport"], preview["paint"], preview["performance_scope"], preview["typography"], preview["headline_css"], preview["css_math"], preview["assets"], preview["font_stacks"], preview["headings"], preview["navigation"], preview["media_queries"], preview["element_examples"], preview["preview_coverage"]})
	return encoded
}

func referencePreview(data map[string]any, sections map[string][]any) map[string]any {
	layout := referenceMap(data["layout"])
	performance := referenceMap(data["performance"])
	viewport, viewportClipped := referencePreviewScalars(layout["viewport"], []string{"width", "height", "device_pixel_ratio", "root_font_px", "scroll_x", "scroll_y"}, 80)
	viewport["json_pointer"] = "/snapshot/reference/layout/viewport"
	navigation, navigationClipped := referencePreviewScalars(performance["navigation"], []string{"type", "ttfb_ms", "dom_content_loaded_ms", "load_ms", "response_end_ms", "transfer_bytes", "decoded_bytes"}, 80)
	paint, paintClipped := referencePreviewScalars(performance["paint"], []string{"first-paint", "first-contentful-paint"}, 80)
	paint["json_pointer"] = "/snapshot/reference/performance/paint"
	groups := referencePreviewGroups(data, sections)
	groupCoverage := map[string]any{}
	baseClipping := map[string]any{"viewport": viewportClipped, "navigation": navigationClipped, "paint": paintClipped}
	preview := map[string]any{
		"viewport":          viewport,
		"navigation":        navigation,
		"paint":             paint,
		"performance_scope": "single navigation, not FPS or a benchmark",
		"preview_coverage": map[string]any{
			"budget_bytes":        referencePreviewBudgetBytes,
			"encoded_bytes":       referencePreviewBudgetBytes,
			"full_evidence":       "reference.read_sections",
			"base_values_clipped": baseClipping,
			"groups":              groupCoverage,
		},
	}
	for _, group := range groups {
		preview[group.name] = []any{}
		groupCoverage[group.name] = map[string]any{"total": group.total, "returned": 0, "omitted": group.total, "records_clipped": 0}
	}
	for index := 0; ; index++ {
		remaining := false
		for _, group := range groups {
			if index >= len(group.items) {
				continue
			}
			remaining = true
			items := preview[group.name].([]any)
			meta := groupCoverage[group.name].(map[string]any)
			returned := meta["returned"].(int)
			clipped := meta["records_clipped"].(int)
			preview[group.name] = append(items, group.items[index].value)
			meta["returned"] = returned + 1
			meta["omitted"] = group.total - returned - 1
			if group.items[index].truncated {
				meta["records_clipped"] = clipped + 1
			}
			encoded, _ := json.Marshal(preview)
			if len(encoded) > referencePreviewBudgetBytes {
				preview[group.name] = items
				meta["returned"] = returned
				meta["omitted"] = group.total - returned
				meta["records_clipped"] = clipped
			}
		}
		if !remaining {
			break
		}
	}
	if referencePreviewEncodedSize(preview) <= referencePreviewBudgetBytes {
		return preview
	}
	// Keep omission counts actionable even if a future base field grows unexpectedly.
	for _, group := range groups {
		groupCoverage[group.name] = map[string]any{"total": group.total, "returned": 0, "omitted": group.total, "records_clipped": 0}
	}
	preview = map[string]any{
		"performance_scope": "single navigation, not FPS or a benchmark",
		"preview_coverage": map[string]any{
			"budget_bytes":        referencePreviewBudgetBytes,
			"encoded_bytes":       0,
			"full_evidence":       "reference.read_sections",
			"base_values_clipped": baseClipping,
			"groups":              groupCoverage,
			"fallback":            "metadata_only",
		},
	}
	referencePreviewEncodedSize(preview)
	return preview
}

func referenceProjection(command *cobra.Command, archive referenceArchive, dir, section string, offset int, flags fastFlags) (fastOutput, error) {
	result := fastOutput{Result: archive.Snapshot, Tab: archive.Tab}
	result.Reference = nil
	var data map[string]any
	if err := json.Unmarshal(archive.Snapshot.Reference, &data); err != nil {
		return result, err
	}
	content := referenceMap(data["content"])
	fullText, _ := content["text"].(string)
	meta := map[string]any{}
	for key, value := range content {
		if key != "text" && key != "headings" && key != "links" && key != "frames" {
			meta[key] = value
		}
	}
	result.Content, _ = json.Marshal(meta)
	result.Coverage, _ = json.Marshal(data["coverage"])
	projection := map[string]any{"id": archive.ID, "section": section, "snapshot_path": filepath.Join(dir, "snapshot.json"), "read": "saved_snapshot"}
	sections := referenceSections(data)
	if section == "content" || section == "overview" {
		if offset > len(fullText) {
			return result, errors.New("offset exceeds saved content bytes")
		}
		if offset < len(fullText) && !utf8.RuneStart(fullText[offset]) {
			return result, errors.New("offset splits a UTF-8 character; use the returned next command")
		}
		result.Text = referenceClip(fullText[offset:], flags.maxText)
		projection["range"] = map[string]any{"offset": offset, "returned_bytes": len(result.Text), "total_bytes": len(fullText)}
		projection["json_pointer"] = "/snapshot/reference/content/text"
		if offset+len(result.Text) < len(fullText) {
			result.Next = referenceReadNext(command, archive.ID, "content", offset+len(result.Text), flags)
		}
	}
	if section == "overview" {
		counts, reads := map[string]int{}, map[string]string{}
		for name, items := range sections {
			counts[name] = len(items)
			reads[name] = referenceReadNext(command, archive.ID, name, 0, flags)
		}
		reads["content"] = referenceReadNext(command, archive.ID, "content", 0, flags)
		projection["counts"], projection["read_sections"] = counts, reads
		projection["artifacts"] = map[string]any{"json": filepath.Join(dir, "snapshot.json"), "markdown": filepath.Join(dir, "content.md"), "screenshot": archive.Screenshot}
		projection["preview"] = referenceOrderedPreview(referencePreview(data, sections))
	} else if section != "content" {
		items, ok := sections[section]
		if !ok {
			return result, errors.New("unknown section; use content, headings, links, frames, elements, styles, fonts, assets, layout, css_math, or performance")
		}
		if offset > len(items) {
			return result, errors.New("offset exceeds saved section records")
		}
		selected, size := []any{}, 0
		for _, item := range items[offset:min(len(items), offset+flags.limit)] {
			encoded, _ := json.Marshal(item)
			if size+len(encoded) > 16384 && len(selected) > 0 {
				break
			}
			if len(encoded) > 16384 {
				selected = append(selected, map[string]any{"inline": false, "reason": "record_exceeds_inline_budget", "record_offset": offset, "artifact": filepath.Join(dir, "snapshot.json"), "bytes": len(encoded)})
				break
			}
			selected = append(selected, item)
			size += len(encoded)
		}
		projection["items"], projection["offset"], projection["total"] = selected, offset, len(items)
		projection["remaining"] = len(items) - offset - len(selected)
		projection["json_pointer"] = "/snapshot/reference/" + section
		if section == "headings" || section == "links" || section == "frames" {
			projection["json_pointer"] = "/snapshot/reference/content/" + section
		}
		pointers := []string{}
		for index, item := range selected {
			pointer := fmt.Sprintf("/snapshot/reference/%s/%d", section, offset+index)
			if section == "performance" {
				pointer = "/snapshot/reference/performance"
			} else if section == "headings" || section == "links" || section == "frames" {
				pointer = fmt.Sprintf("/snapshot/reference/content/%s/%d", section, offset+index)
			} else if section == "css_math" {
				pointer = fmt.Sprintf("/snapshot/reference/layout/css_math/%d", offset+index)
			} else if section == "fonts" || section == "layout" {
				pointer, _ = referenceMap(item)["json_pointer"].(string)
			}
			pointers = append(pointers, pointer)
		}
		projection["item_json_pointers"] = pointers
		if section == "css_math" {
			projection["json_pointer"] = "/snapshot/reference/layout/css_math"
		}
		if offset+len(selected) < len(items) {
			result.Next = referenceReadNext(command, archive.ID, section, offset+len(selected), flags)
		}
	}
	result.Reference, _ = json.Marshal(projection)
	return result, nil
}

func newBrowserReferenceCommand(deps jevDOMDependencies) *cobra.Command {
	flags := fastFlags{}
	var saved, section, selector string
	var offset int
	var screenshot, keepTab bool
	command := &cobra.Command{Use: "reference [url]", Short: "Collect page content, CSS, fonts, assets and timings into a reusable reference", Args: cobra.MaximumNArgs(1)}
	command.SilenceUsage, command.SilenceErrors = true, true
	command.RunE = func(command *cobra.Command, args []string) (returnErr error) {
		started := time.Now()
		// Phase timings attribute the wall time between navigation and output so a
		// slow read can be blamed on the browser, the page, or Rep's own work.
		phases, lastPhase := map[string]int64{}, started
		phase := func(name string) {
			now := time.Now()
			phases[name] += now.Sub(lastPhase).Milliseconds()
			lastPhase = now
		}
		if err := validateFastFlags(flags); err != nil {
			return reportJevError(command, err)
		}
		if offset < 0 || len(selector) > 2048 {
			return reportJevError(command, errors.New("offset must be nonnegative and selector at most 2048 bytes"))
		}
		if saved != "" {
			if len(args) != 0 || flags.tab >= 0 || selector != "" {
				return reportJevError(command, errors.New("--saved reads a task snapshot; do not combine it with a URL, tab, or selector"))
			}
			archive, dir, err := loadReference(saved)
			if err != nil {
				return reportJevError(command, err)
			}
			result, err := referenceProjection(command, archive, dir, section, offset, flags)
			result.DurationMS = time.Since(started).Milliseconds()
			return emitFast(command, result, err)
		}
		if section != "overview" || offset != 0 {
			return reportJevError(command, errors.New("--section and --offset read a --saved snapshot returned by reference"))
		}
		rawURL, err := fastTarget(args, flags.tab)
		if err != nil {
			return reportJevError(command, err)
		}
		record, err := beginBrowserEvidence("browser.reference", map[string]any{"intent": "collect reusable page reference", "navigation": rawURL != ""}, decisionBrowserName(command), flags.tab)
		if err != nil {
			return reportJevError(command, err)
		}
		defer record.finishOnReturn(&returnErr)
		ctx, cancel := context.WithTimeout(command.Context(), flags.timeout)
		defer cancel()
		browser, err := deps.browser(ctx, decisionBrowserName(command))
		if err != nil {
			return reportJevError(command, err)
		}
		owner, err := decisionOwner()
		if err != nil {
			return reportJevError(command, err)
		}
		phase("connect")
		record.dispatch()
		// The reference renderer waits for DOM readiness within its own lease.
		// Hand off after commit so navigation does not hold a temporary debugger
		// attachment across the entire readiness budget or a continuation.
		session, created, err := fastSessionWithWait(ctx, browser, owner, rawURL, flags.tab, "commit")
		phase("tab_navigate_lease")
		result := fastOutput{Tab: flags.tab, Created: created, Phases: phases}
		var owned *referenceOwnedTab
		if session != nil {
			result.Tab = session.Tab
			result.Next = referenceResume(command, session.Tab, selector, screenshot)
			owned = referenceLoadTab(session.Tab, decisionBrowserName(command))
			if created {
				owned = &referenceOwnedTab{Tab: session.Tab, Browser: decisionBrowserName(command), Created: time.Now().UTC(), Keep: keepTab}
			}
			if owned != nil {
				owned.Keep = owned.Keep || keepTab
				if saveErr := referenceSaveTab(owned); err == nil {
					err = saveErr
				}
			}
		}
		if err == nil {
			read := func() (browserfast.Result, error) {
				return session.Eval(ctx, browserfast.Spec{Operation: "reference", Selector: selector, WaitMS: max(0, time.Until(deadlineOrNow(ctx)).Milliseconds()-200)})
			}
			result.Result, err = read()
			if rawURL != "" {
				err = rebindAfterURLChange(ctx, session, &result, err, read)
			}
			phase("ready_and_evaluate")
			if err == nil && result.Status == "failed" {
				err = errors.New(result.Code)
			}
			if err == nil && owned != nil && owned.URL == "" && result.URL != "" {
				owned.URL = result.URL
				err = referenceSaveTab(owned)
			}
		}
		if err == nil && len(result.Reference) > 0 {
			var nonce [16]byte
			_, err = rand.Read(nonce[:])
			id := "r_" + hex.EncodeToString(nonce[:])
			dir, pathErr := referencePath(id)
			if err == nil {
				err = pathErr
			}
			archive := referenceArchive{Version: 1, ID: id, Workspace: "", Browser: decisionBrowserName(command), Tab: session.Tab, Snapshot: result.Result,
				Screenshot: map[string]any{"status": "not_requested"}}
			selected, _ := scope.Current()
			archive.Workspace, archive.Task = selected.Workspace, selected.Task
			if err == nil && screenshot {
				archive.Screenshot = map[string]any{"status": "not_captured", "reason": "remaining_budget", "next": fastNext(command, "screenshot", session.Tab, "")}
				if time.Until(deadlineOrNow(ctx)) > 300*time.Millisecond {
					var image struct {
						Data string `json:"data"`
					}
					// JPEG at quality 80 encodes in about 50 ms where the same viewport PNG
					// takes 180 to 300 ms and four times the bytes; the image is context for
					// design research, not pixel evidence. Use browser screenshot for PNG.
					shotErr := session.Inspect(ctx, result.URL, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 80, "captureBeyondViewport": false}, &image)
					if shotErr == nil {
						bytes, decodeErr := base64.StdEncoding.DecodeString(image.Data)
						if decodeErr == nil && len(bytes) > 0 && len(bytes) <= 32*1024*1024 {
							artifact, saveErr := referenceFile(filepath.Join(dir, "viewport.jpg"), bytes)
							if saveErr == nil {
								archive.Screenshot = map[string]any{"status": "captured", "artifact": artifact, "format": "jpeg", "scope": "viewport after DOM observation", "captured_at": time.Now().UTC().Format(time.RFC3339Nano)}
							} else {
								err = saveErr
							}
						} else {
							archive.Screenshot["reason"] = "invalid_screenshot_bytes"
						}
					} else {
						archive.Screenshot["reason"] = "screenshot_unavailable"
					}
				}
				phase("screenshot")
			}
			if err == nil {
				if owned != nil && !owned.Keep && result.Status == "ready" {
					delete(archive.Screenshot, "next")
				}
				encoded, encodeErr := json.Marshal(archive)
				if encodeErr != nil {
					err = encodeErr
				} else if len(encoded) > 8*1024*1024 {
					err = errors.New("reference exceeds artifact byte budget")
				} else {
					_, err = referenceFile(filepath.Join(dir, "snapshot.json"), encoded)
				}
			}
			if err == nil {
				var data map[string]any
				_ = json.Unmarshal(archive.Snapshot.Reference, &data)
				content := referenceMap(data["content"])
				text, _ := content["text"].(string)
				markdown := fmt.Sprintf("# %s\n\nSource: <%s>\nObserved: %s\nContent source: %v; scope: %v; status: %v; complete within scope: %v\n\n%s\n", strings.ReplaceAll(result.Title, "\n", " "), result.URL, result.ObservedAt, content["source"], content["scope"], content["status"], content["complete"], text)
				_, err = referenceFile(filepath.Join(dir, "content.md"), []byte(markdown))
			}
			phase("write_artifacts")
			if err == nil {
				next := result.Next
				result, err = referenceProjection(command, archive, dir, "overview", 0, flags)
				result.Created = created
				result.Phases = phases
				if result.Status == "loading" {
					result.Next = next
				}
			}
			phase("project_preview")
		}
		if session != nil {
			if err == nil {
				referenceFinishTab(session, owned, &result, fastNext(command, "page", session.Tab, ""))
			}
			if closeErr := session.Close(); err == nil {
				err = closeErr
			}
			phase("close_tab_and_lease")
			result.BridgeCalls = session.Calls
		}
		if result.Tab >= 0 && fastReadPending(ctx, err) {
			result.Status, result.Code, err = "loading", "observation_pending", nil
			result.Next = referenceResume(command, result.Tab, selector, screenshot)
		}
		return finishFast(command, result, record, started, err)
	}
	bindFastFlags(command, &flags)
	command.Flags().StringVar(&selector, "selector", "", "Focus design and content inspection on one unique CSS selector")
	command.Flags().BoolVar(&screenshot, "screenshot", true, "Save a viewport image when the observation budget permits")
	command.Flags().BoolVar(&keepTab, "keep-tab", false, "Keep this task-created reference tab for further live inspection")
	command.Flags().StringVar(&saved, "saved", "", "Read this task's immutable reference snapshot without browser work")
	command.Flags().StringVar(&section, "section", "overview", "Saved section: content, headings, links, frames, elements, styles, fonts, assets, layout, css_math, performance")
	command.Flags().IntVar(&offset, "offset", 0, "Saved content byte offset, or section record offset (use returned next)")
	return command
}

func init() { browserCmd.AddCommand(newBrowserReferenceCommand(defaultJevDOMDependencies())) }
