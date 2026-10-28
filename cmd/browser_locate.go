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
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/imaging"
	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/visual"
	"github.com/spf13/cobra"
)

// browser locate grounds a goal visually: one viewport screenshot and one DOM
// layout snapshot go to Jev as a box question plus a presence check, and the
// returned image box is mapped onto real DOM geometry. It selects; it never acts.

const locateReviewThreshold = 0.8
const locateMinimumOverlap = 0.2

type locateNode struct {
	BackendDOMNodeID int64       `json:"backend_dom_node_id"`
	Tag              string      `json:"tag"`
	Role             string      `json:"role,omitempty"`
	Name             string      `json:"name,omitempty"`
	Text             string      `json:"text,omitempty"`
	Clickable        bool        `json:"clickable"`
	Box              visual.Rect `json:"box_css_px"`
	IoU              float64     `json:"iou"`
	Coverage         float64     `json:"box_coverage"`
}

type locateViewport struct {
	Width            float64 `json:"width_css_px"`
	Height           float64 `json:"height_css_px"`
	ScrollX          float64 `json:"scroll_x"`
	ScrollY          float64 `json:"scroll_y"`
	DevicePixelRatio float64 `json:"device_pixel_ratio"`
}

type locateScreenshot struct {
	Path           string  `json:"path"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Bytes          int     `json:"bytes"`
	SHA256         string  `json:"sha256"`
	PixelsPerCSSPx float64 `json:"pixels_per_css_px"`
	Scope          string  `json:"scope"`
}

type locateBox struct {
	Fractions jev.Box     `json:"fractions"`
	CSS       visual.Rect `json:"css_px"`
}

type locateCoverage struct {
	Documents          int     `json:"documents_in_snapshot"`
	LayoutNodes        int     `json:"layout_nodes"`
	ElementsConsidered int     `json:"elements_considered"`
	Skipped            int     `json:"elements_outside_viewport"`
	SnapshotScale      float64 `json:"snapshot_units_per_css_px"`
	TopDocumentOnly    bool    `json:"top_document_only"`
}

type locateOutput struct {
	Status            string                `json:"status"`
	Code              string                `json:"code,omitempty"`
	Version           int                   `json:"version"`
	Workspace         string                `json:"workspace"`
	Task              string                `json:"task"`
	URL               string                `json:"url,omitempty"`
	Title             string                `json:"title,omitempty"`
	Tab               int                   `json:"tab_id"`
	Goal              string                `json:"goal"`
	Presence          *float64              `json:"presence_probability,omitempty"`
	BoxConfidence     *float64              `json:"box_confidence,omitempty"`
	Box               *locateBox            `json:"box,omitempty"`
	Node              *locateNode           `json:"node,omitempty"`
	Control           *locateNode           `json:"control,omitempty"`
	Alternatives      []locateNode          `json:"alternatives,omitempty"`
	Viewport          *locateViewport       `json:"viewport,omitempty"`
	Screenshot        *locateScreenshot     `json:"screenshot,omitempty"`
	Coverage          *locateCoverage       `json:"coverage,omitempty"`
	Model             string                `json:"model,omitempty"`
	Usage             jev.Usage             `json:"usage"`
	ModelRequests     int                   `json:"model_requests"`
	BridgeCalls       int                   `json:"bridge_calls"`
	DurationMS        int64                 `json:"duration_ms"`
	Phases            map[string]int64      `json:"phases_ms,omitempty"`
	OperationEvidence *operationEvidenceRef `json:"operation_evidence,omitempty"`
	Next              string                `json:"next,omitempty"`
	Error             *output.AgentError    `json:"error,omitempty"`
}

type locatePageInfo struct {
	URL              string  `json:"href"`
	Title            string  `json:"title"`
	DevicePixelRatio float64 `json:"dpr"`
	Width            float64 `json:"w"`
	Height           float64 `json:"h"`
	ScrollX          float64 `json:"x"`
	ScrollY          float64 `json:"y"`
	ScrollWidth      float64 `json:"sw"`
}

const locatePageInfoExpression = `({href:location.href,title:document.title,dpr:devicePixelRatio,w:innerWidth,h:innerHeight,x:scrollX,y:scrollY,sw:document.documentElement.scrollWidth})`

func locateDescribe(scene visual.Scene, element visual.Element, iou, coverage float64) locateNode {
	node := locateNode{BackendDOMNodeID: element.BackendNodeID, Tag: strings.ToLower(element.Tag), Clickable: element.Clickable, Box: element.Box, IoU: math.Round(iou*1000) / 1000, Coverage: math.Round(coverage*1000) / 1000, Text: element.Text}
	node.Role = element.Attributes["role"]
	if node.Role == "" && element.Clickable && !interactiveTag(element.Tag) {
		node.Role = "clickable"
	}
	if node.Role == "" {
		switch element.Tag {
		case "A":
			node.Role = "link"
		case "BUTTON", "SUMMARY":
			node.Role = "button"
		case "SELECT":
			node.Role = "combobox"
		case "TEXTAREA":
			node.Role = "textbox"
		case "INPUT":
			switch strings.ToLower(element.Attributes["type"]) {
			case "checkbox", "radio", "button", "submit", "reset":
				node.Role = strings.ToLower(element.Attributes["type"])
			case "search":
				node.Role = "searchbox"
			default:
				node.Role = "textbox"
			}
		}
	}
	for _, key := range []string{"aria-label", "title", "alt", "placeholder", "name"} {
		if value := element.Attributes[key]; value != "" {
			node.Name = value
			break
		}
	}
	if node.Name == "" {
		node.Name = scene.Label(element, 80)
	}
	return node
}

func interactiveTag(tag string) bool {
	switch tag {
	case "A", "BUTTON", "INPUT", "SELECT", "TEXTAREA", "SUMMARY", "LABEL", "OPTION":
		return true
	}
	return false
}

// locatePath compares documents by origin and path. Applications such as
// Google Docs rewrite the query or fragment after commit; that is the same
// document for a read, while a different path is a different page.
// captureViewportImage reads the viewport geometry and captures it as a JPEG
// scaled to one image pixel per CSS pixel within the model's limits. It is the
// shared observation step of browser locate and browser state. The returned
// code names the failing step for the agent-facing error.
func captureViewportImage(ctx context.Context, session *browserfast.Session) (locatePageInfo, []byte, jev.Image, float64, string, error) {
	var page locatePageInfo
	var evaluated struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := session.Inspect(ctx, "", "Runtime.evaluate", map[string]any{"expression": locatePageInfoExpression, "returnByValue": true}, &evaluated); err != nil {
		return page, nil, jev.Image{}, 0, "", err
	}
	if json.Unmarshal(evaluated.Result.Value, &page) != nil || page.Width <= 0 || page.Height <= 0 || page.DevicePixelRatio <= 0 {
		return page, nil, jev.Image{}, 0, "page_geometry_unavailable", errors.New("browser returned no usable viewport geometry")
	}
	// Capture the full viewport as JPEG (about 50 ms at device resolution,
	// versus 180 ms for PNG) and scale it in Go: clipped, scaled captures
	// return empty data on some Chromium builds. The target is one image pixel
	// per CSS pixel when the documented limits allow, so box fractions map
	// onto CSS coordinates directly.
	pixelsPerCSS := jev.FitScale(page.Width, page.Height)
	var shot struct {
		Data string `json:"data"`
	}
	if err := session.Inspect(ctx, "", "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 85, "captureBeyondViewport": false}, &shot); err != nil {
		return page, nil, jev.Image{}, 0, "", err
	}
	captured, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil || len(captured) == 0 {
		return page, nil, jev.Image{}, 0, "invalid_screenshot_bytes", errors.New("browser returned invalid screenshot data")
	}
	fitted, err := imaging.FitJPEG(captured, math.Min(1, pixelsPerCSS/page.DevicePixelRatio), 85)
	if err != nil {
		return page, nil, jev.Image{}, 0, "screenshot_resize_failed", err
	}
	image, err := jev.DecodeImage(fitted.Data)
	if err != nil {
		return page, nil, jev.Image{}, 0, "screenshot_outside_model_limits", err
	}
	pixelsPerCSS = float64(image.Width) / page.Width
	if math.Abs(float64(image.Height)/page.Height-pixelsPerCSS) > 0.02 {
		return page, nil, jev.Image{}, 0, "screenshot_geometry_unexpected", fmt.Errorf("screenshot is %dx%d for a %.0fx%.0f viewport", image.Width, image.Height, page.Width, page.Height)
	}
	return page, fitted.Data, image, pixelsPerCSS, "", nil
}

func locatePath(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}

func newBrowserLocateCommand(deps jevDOMDependencies) *cobra.Command {
	var tab int
	var timeout time.Duration
	command := &cobra.Command{Use: "locate <goal> --tab ID", Short: "Find where a described thing is on the visible page from a screenshot, with its DOM node", Args: cobra.ExactArgs(1)}
	command.SilenceUsage, command.SilenceErrors = true, true
	command.RunE = func(command *cobra.Command, args []string) (returnErr error) {
		started := time.Now()
		goal := strings.TrimSpace(args[0])
		if goal == "" || len(goal) > 2000 || tab < 0 || timeout < time.Second || timeout > 60*time.Second {
			return reportJevError(command, errors.New("provide a goal of 1 to 2000 bytes, --tab, and a timeout between 1s and 60s"))
		}
		// Fail before any browser work when no decision provider is configured.
		config, err := deps.loadConfig()
		if err != nil {
			return reportJevError(command, err)
		}
		if deps.asker == nil {
			return reportJevError(command, errors.New("no Jev client is available"))
		}
		record, err := beginBrowserEvidence("browser.locate", map[string]any{"intent": "visually locate a described page element", "goal_bytes": len(goal)}, decisionBrowserName(command), tab)
		if err != nil {
			return reportJevError(command, err)
		}
		defer record.finishOnReturn(&returnErr)
		ctx, cancel := context.WithTimeout(command.Context(), timeout)
		defer cancel()
		result := locateOutput{Version: 1, Tab: tab, Goal: goal, Phases: map[string]int64{}, Next: fastNext(command, "page", tab, "")}
		selected, _ := scope.Current()
		result.Workspace, result.Task = selected.Workspace, selected.Task
		last := started
		phase := func(name string) {
			now := time.Now()
			result.Phases[name] = now.Sub(last).Milliseconds()
			last = now
		}
		finish := func(err error) error {
			result.DurationMS = time.Since(started).Milliseconds()
			result.OperationEvidence = record.ref()
			status, verification := "completed", "unverified"
			if err != nil {
				status, verification = "failed", "unknown"
				if result.Status == "" {
					result.Status = "failed"
				}
			}
			if evidenceErr := record.finish(map[string]any{"result": result, "verification_scope": "visual_selection"}, status, verification, "", err); evidenceErr != nil {
				err = errors.Join(err, evidenceErr)
			}
			if err != nil {
				ae := jevAgentError(command.CommandPath(), err)
				if result.Code != "" {
					ae.Code = result.Code
				}
				ae.Suggest = append(ae.Suggest, result.Next)
				result.Error = &ae
			}
			encoder := json.NewEncoder(command.OutOrStdout())
			encoder.SetEscapeHTML(false)
			var data any = result
			if forceEnvelope && !rawJSON {
				data = output.WrapData(command.CommandPath(), "browser", data)
			}
			if encodeErr := encoder.Encode(data); encodeErr != nil {
				return encodeErr
			}
			if err != nil {
				return output.MarkReported(*result.Error)
			}
			return nil
		}
		browser, err := deps.browser(ctx, decisionBrowserName(command))
		if err != nil {
			return finish(err)
		}
		owner, err := decisionOwner()
		if err != nil {
			return finish(err)
		}
		phase("connect")
		session, _, err := fastSession(ctx, browser, owner, "", tab)
		if session != nil {
			defer func() {
				if closeErr := session.Close(); closeErr != nil && returnErr == nil {
					returnErr = closeErr
				}
				result.BridgeCalls = session.Calls
			}()
		}
		if err != nil {
			return finish(err)
		}
		phase("lease")
		page, imageBytes, image, pixelsPerCSS, code, err := captureViewportImage(ctx, session)
		if err != nil {
			result.Code = code
			return finish(err)
		}
		result.URL, result.Title = page.URL, page.Title
		result.Viewport = &locateViewport{Width: page.Width, Height: page.Height, ScrollX: page.ScrollX, ScrollY: page.ScrollY, DevicePixelRatio: page.DevicePixelRatio}
		phase("page_info")
		phase("screenshot")
		var snapshot json.RawMessage
		if err := session.Inspect(ctx, "", "DOMSnapshot.captureSnapshot", map[string]any{"computedStyles": []string{}, "includeDOMRects": false, "includePaintOrder": false}, &snapshot); err != nil {
			return finish(err)
		}
		var after struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := session.Inspect(ctx, "", "Runtime.evaluate", map[string]any{"expression": "location.href", "returnByValue": true}, &after); err != nil {
			return finish(err)
		}
		if locatePath(after.Result.Value) != locatePath(page.URL) {
			result.Code = "page_changed"
			return finish(fmt.Errorf("the document moved from %s to %s during observation", locatePath(page.URL), locatePath(after.Result.Value)))
		}
		result.URL = after.Result.Value
		scene, err := visual.Parse(snapshot, visual.Viewport{Width: page.Width, Height: page.Height, ScrollX: page.ScrollX, ScrollY: page.ScrollY, ScrollWidth: page.ScrollWidth})
		if err != nil {
			result.Code = "dom_snapshot_unavailable"
			return finish(err)
		}
		result.Coverage = &locateCoverage{Documents: scene.Documents, LayoutNodes: scene.LayoutNodes, ElementsConsidered: len(scene.Elements), Skipped: scene.Skipped, SnapshotScale: scene.Scale, TopDocumentOnly: true}
		phase("dom_snapshot")
		path, err := saveTaskArtifact("locate", "shot-*.jpg", imageBytes)
		if err != nil {
			return finish(err)
		}
		sum := sha256.Sum256(imageBytes)
		result.Screenshot = &locateScreenshot{Path: path, Width: image.Width, Height: image.Height, Bytes: len(imageBytes), SHA256: hex.EncodeToString(sum[:]), PixelsPerCSSPx: pixelsPerCSS, Scope: "viewport"}
		phase("write")
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return finish(err)
		}
		asset := "shot-" + hex.EncodeToString(nonce[:])
		// The goal is treated as data inside the instructions; the state carries
		// only the image and its dimensions, never page text or URLs.
		request := jev.AskRequest{
			State: map[string]any{"screenshot": asset, "screenshot_size": map[string]int{"width": image.Width, "height": image.Height}},
			Questions: map[string]jev.TypedQuestion{
				"location": {Type: "box", Instructions: fmt.Sprintf("Where is this in the image `screenshot`: %s", goal)},
				"visible":  {Type: "noul", Instructions: fmt.Sprintf("Is this visible in the image `screenshot`: %s", goal)},
			},
			Assets: map[string]jev.Image{asset: image},
		}
		result.ModelRequests = 1
		response, err := deps.asker(config).Ask(ctx, request)
		if err != nil {
			return finish(err)
		}
		result.Model, result.Usage = response.Model, response.Usage
		phase("model")
		location, visible := response.Answers["location"], response.Answers["visible"]
		result.Presence, result.BoxConfidence = visible.Noul, location.Confidence
		if location.Box == nil || visible.Noul == nil {
			result.Code = "incomplete_decision"
			return finish(errors.New("Jev returned no box or presence answer"))
		}
		css := visual.Rect{X: location.Box.Left * page.Width, Y: location.Box.Top * page.Height, Width: (location.Box.Right - location.Box.Left) * page.Width, Height: (location.Box.Bottom - location.Box.Top) * page.Height}
		result.Box = &locateBox{Fractions: *location.Box, CSS: css}
		matches := scene.Rank(css, 4)
		if len(matches) > 0 {
			best := locateDescribe(scene, matches[0].Element, matches[0].IoU, matches[0].Coverage)
			result.Node = &best
			if control := scene.Control(matches[0].Element, css); control != nil {
				described := locateDescribe(scene, *control, visual.IoU(control.Box, css), 0)
				result.Control = &described
			}
			for _, match := range matches[1:] {
				result.Alternatives = append(result.Alternatives, locateDescribe(scene, match.Element, match.IoU, match.Coverage))
			}
		}
		phase("mapping")
		switch {
		case *visible.Noul < 0.5:
			result.Status, result.Code = "not_visible", "goal_not_visible"
		case *visible.Noul < locateReviewThreshold, location.Confidence != nil && *location.Confidence < 0.5, result.Node == nil, result.Node.IoU < locateMinimumOverlap:
			result.Status, result.Code = "needs_review", "weak_visual_match"
		default:
			result.Status = "located"
		}
		return finish(nil)
	}
	command.Flags().IntVar(&tab, "tab", -1, "Existing task-owned tab to inspect without navigation")
	command.Flags().DurationVar(&timeout, "timeout", 15*time.Second, "Observation and decision budget (1s to 60s)")
	_ = command.MarkFlagRequired("tab")
	return command
}

func init() {
	command := newBrowserLocateCommand(defaultJevDOMDependencies())
	addDecisionBrowserFlag(command)
	browserCmd.AddCommand(command)
}
