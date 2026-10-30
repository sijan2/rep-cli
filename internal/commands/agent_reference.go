package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

func newCoreReferenceCommand() *cobra.Command {
	options := corePageOptions{Reference: true}
	command := &cobra.Command{Use: "reference URL|TAB|p_CURSOR", Short: "Save content, layout, fonts, assets and a screenshot", Args: cobra.ExactArgs(1)}
	command.Flags().StringVar(&options.Selector, "selector", "", "Focus one CSS selector or control reference")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := corePage(cmd, args, options); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreReadReference(ctx context.Context, command *cobra.Command, store *agentstate.Store, tab *agentstate.Tab, lease string, session *browserfast.Session, cursor *agentstate.Cursor, options corePageOptions) error {
	if cursor != nil && cursor.Selector != "" {
		options.Selector = cursor.Selector
	}
	selector := options.Selector
	if agentstate.RefPattern.MatchString(selector) {
		reference, err := store.ResolveRef(selector)
		if err != nil {
			return err
		}
		if agentstate.TabOf(selector) != tab.Handle {
			return coreArgument("selector reference belongs to another tab")
		}
		selector = reference.Raw
	}
	observed, err := session.Eval(ctx, browserfast.Spec{Operation: "reference", Selector: selector, MaxText: 10000, Limit: 40, WaitMS: 6000, ReadMS: 6000})
	if err != nil {
		return err
	}
	if observed.Status == "failed" {
		return agentstate.Failure(observed.Code, "reference extraction failed", coreNext("page", tab.Handle))
	}
	var screenshot struct {
		Data string `json:"data"`
	}
	if err := session.Inspect(ctx, "", "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 80, "captureBeyondViewport": false}, &screenshot); err != nil {
		return err
	}
	image, err := base64.StdEncoding.DecodeString(screenshot.Data)
	if err != nil {
		return err
	}
	if len(image) == 0 {
		return coreErrorf("screenshot_unavailable", "browser returned no screenshot bytes")
	}
	dir, err := referencePath(agentstate.Token("r_"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "screenshot.jpg")
	if err := writePrivateFile(path, image); err != nil {
		return err
	}
	digest := sha256.Sum256(image)
	result := coreTabResult(tab)
	result.Status = observed.Status
	result.Meta = map[string]any{"screenshot": map[string]any{"path": path, "bytes": len(image), "sha256": hex.EncodeToString(digest[:])}}
	if observed.Status == "loading" {
		result.Complete = boolPointer(false)
		if cursor != nil {
			result.Next = coreNext("shot", tab.Handle)
		} else {
			id, e := store.SaveCursor(agentstate.Cursor{Tab: tab.Handle, Owner: tab.Owner, Kind: "reference-loading", Selector: options.Selector, Budget: 10000})
			if e != nil {
				return e
			}
			result.Next = coreNext("reference", id)
		}
		return emitCore(command, result)
	}
	var data map[string]any
	if err := json.Unmarshal(observed.Reference, &data); err != nil {
		return err
	}
	safe := output.RedactValue(data)
	encoded, err := output.Marshal(safe)
	if err != nil {
		return err
	}
	snapshot := filepath.Join(dir, "snapshot.json")
	if err := writePrivateFile(snapshot, encoded); err != nil {
		return err
	}
	sections := referenceSections(data)
	records := []map[string]any{}
	content := referenceMap(data["content"])
	text, _ := content["text"].(string)
	for len(text) > 0 {
		chunk := referenceClip(text, 8000)
		records = append(records, map[string]any{"section": "content", "data": chunk})
		text = text[len(chunk):]
	}
	for _, section := range []string{"headings", "links", "frames", "elements", "styles", "fonts", "assets", "layout", "css_math", "performance"} {
		for _, item := range sections[section] {
			records = append(records, map[string]any{"section": section, "data": item})
		}
	}
	recordsPath := filepath.Join(dir, "sections.json")
	encoded, err = output.Marshal(output.RedactValue(records))
	if err != nil {
		return err
	}
	if err := writePrivateFile(recordsPath, encoded); err != nil {
		return err
	}
	if len(records) > 0 {
		id, e := store.SaveCursor(agentstate.Cursor{Tab: tab.Handle, Owner: tab.Owner, Kind: "reference-sections", Source: recordsPath, Budget: 10000})
		if e != nil {
			return e
		}
		result.Next = coreNext("reference", id)
	}
	result.Complete = boolPointer(len(records) == 0)
	result.Meta.(map[string]any)["snapshot_path"] = snapshot
	result.Untrusted = map[string]any{"url": observed.URL, "title": observed.Title, "preview": referenceOrderedPreview(referencePreview(data, sections)), "content": referenceClip(textFromReference(content), 2000)}
	if err := store.Change(tab.Handle, lease, func(tab *agentstate.Tab) error { tab.URL = observed.URL; return nil }); err != nil {
		return err
	}
	return emitCore(command, result)
}
func textFromReference(content map[string]any) string {
	value, _ := content["text"].(string)
	return value
}
func coreReferenceContinuation(command *cobra.Command, store *agentstate.Store, cursor agentstate.Cursor) error {
	data, err := os.ReadFile(cursor.Source)
	if err != nil {
		return err
	}
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		return err
	}
	if cursor.Offset < 0 || cursor.Offset > len(records) {
		return coreArgument("invalid reference cursor offset")
	}
	selected := []map[string]any{}
	size := 0
	offset := cursor.Offset
	for offset < len(records) {
		encoded, _ := output.Marshal(records[offset])
		if size+len(encoded) > 8000 && len(selected) > 0 {
			break
		}
		if len(encoded) > 8000 {
			selected = append(selected, map[string]any{"section": records[offset]["section"], "artifact_path": cursor.Source, "record_offset": offset, "bytes": len(encoded)})
			offset++
			break
		}
		selected = append(selected, records[offset])
		size += len(encoded)
		offset++
	}
	result := coreResult{Tab: cursor.Tab, Status: "ready", Complete: boolPointer(offset == len(records)), Meta: map[string]any{"returned": len(selected), "total": len(records), "source": cursor.Source}, Untrusted: map[string]any{"sections": selected}}
	if offset < len(records) {
		cursor.Offset = offset
		id, e := store.SaveCursor(cursor)
		if e != nil {
			return e
		}
		result.Next = coreNext("reference", id)
	}
	if tab, e := store.Tab(cursor.Tab); e == nil {
		result.Browser, result.Profile = tab.Browser.Name, tab.Browser.Profile
	}
	return emitCore(command, result)
}

func coreLegacyReferenceContinuation(command *cobra.Command, store *agentstate.Store, cursor agentstate.Cursor) error {
	archive, dir, err := loadReference(cursor.Source)
	if err != nil {
		return err
	}
	projection, err := referenceProjection(command, archive, dir, cursor.Section, cursor.Offset, fastFlags{maxText: cursor.Budget, limit: cursor.Controls})
	if err != nil {
		return err
	}
	return emitCore(command, coreResult{Status: projection.Status, Complete: boolPointer(projection.Next == ""), Next: projection.Next, Untrusted: map[string]any{"reference": decodeObject(projection.Reference), "text": projection.Text, "url": projection.URL, "title": projection.Title}})
}
