package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/browserrpc"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

type coreShotOptions struct {
	URLs, Viewport, Selector string
	Full                     bool
}

func newCoreShotCommand() *cobra.Command {
	options := coreShotOptions{}
	command := &cobra.Command{Use: "shot [TAB|URL] [PATH]", Short: "Save screenshots", Args: cobra.MaximumNArgs(2)}
	command.Flags().StringVar(&options.URLs, "urls", "", "Comma-separated URLs or @file with one URL per line")
	command.Flags().StringVar(&options.Viewport, "viewport", "", "desktop, mobile, or WIDTHxHEIGHT")
	command.Flags().BoolVar(&options.Full, "full-page", false, "Capture the scrollable page")
	command.Flags().StringVar(&options.Selector, "selector", "", "Capture one CSS selector or observed reference")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreShot(cmd, args, options); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreShot(command *cobra.Command, args []string, options coreShotOptions) error {
	if options.Viewport != "" {
		if _, _, err := coreViewport(options.Viewport); err != nil {
			return err
		}
	}
	urls := []string{}
	path, target := "", ""
	if options.URLs != "" {
		if strings.HasPrefix(options.URLs, "@") {
			text, err := coreSource(command, options.URLs)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					urls = append(urls, line)
				}
			}
		} else {
			urls = strings.Split(options.URLs, ",")
		}
		if len(args) > 1 {
			return coreArgument("a URL sweep accepts only an output directory")
		}
		if len(args) > 0 {
			path = args[0]
		}
	}
	if options.URLs == "" && len(args) > 0 {
		if coreHTTPURL(args[0]) {
			urls = append(urls, args[0])
		} else if agentstate.TabPattern.MatchString(args[0]) {
			target = args[0]
		} else {
			path = args[0]
		}
		if len(args) > 1 {
			path = args[1]
		}
	}
	if len(urls) > 0 {
		if options.Selector != "" && agentstate.RefPattern.MatchString(options.Selector) {
			return coreArgument("a reference cannot target a newly navigated URL")
		}
		return coreShotURLs(command, urls, path, options)
	}
	store, err := coreStore()
	if err != nil {
		return err
	}
	var addressed *agentstate.Tab
	if target != "" {
		addressed, err = store.Tab(target)
		if err != nil {
			return err
		}
	}
	ctx, cancel := coreTimeout(command, 15*time.Second)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	tab, lease, err := store.Reserve(browser, selected.Identity(), selected.SessionKey, target, selected.TaskSource == "flag", false, 16*time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	if tab.ChromeID < 0 {
		return agentstate.Failure("tab_required", "open a page before taking a screenshot", coreNext("tabs"))
	}
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: tab.Owner}
	defer session.Close()
	if err = session.Acquire(ctx); err != nil {
		return err
	}
	if err = applyCoreViewport(ctx, store, tab, lease, session, options.Viewport); err != nil {
		return err
	}
	call := func(method string, params, out any) error { return session.Inspect(ctx, "", method, params, out) }
	asset, err := coreTakeShot(call, store, tab, options, path)
	if err != nil {
		return err
	}
	result := coreTabResult(tab)
	result.Status = "saved"
	result.Complete = boolPointer(true)
	result.Meta = asset
	return emitCore(command, result)
}
func coreTakeShot(call func(string, any, any) error, store *agentstate.Store, tab *agentstate.Tab, options coreShotOptions, path string) (map[string]any, error) {
	format := "png"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpeg", ".jpg":
		format = "jpeg"
	case ".webp":
		format = "webp"
	case "", ".png":
	default:
		return nil, coreArgument("image path must end in .png, .jpg, .jpeg, or .webp")
	}
	params := map[string]any{"format": format, "captureBeyondViewport": options.Full, "fromSurface": true}
	if format != "png" {
		params["quality"] = 80
	}
	if options.Full {
		var metrics struct {
			Content struct{ Width, Height float64 } `json:"cssContentSize"`
		}
		if err := call("Page.getLayoutMetrics", map[string]any{}, &metrics); err != nil {
			return nil, err
		}
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": metrics.Content.Width, "height": metrics.Content.Height, "scale": 1}
	}
	if options.Selector != "" {
		selector, _ := json.Marshal(options.Selector)
		expression := "document.querySelector(" + string(selector) + ")"
		if agentstate.RefPattern.MatchString(options.Selector) {
			if agentstate.TabOf(options.Selector) != tab.Handle {
				return nil, coreArgument("reference belongs to a different tab")
			}
			ref, err := store.ResolveRef(options.Selector)
			if err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(ref.Raw)
			owner, _ := json.Marshal(tab.Owner)
			expression = "(()=>{const b=globalThis[Symbol.for('rep.fast.v1')]?.owners.get(" + string(owner) + ")?.refs.get(" + string(raw) + ");if(!b||b.generation!==performance.timeOrigin||!b.el.isConnected)throw Error('stale reference');return b.el})()"
		}
		expression = "(()=>{const e=" + expression + ";if(!e)throw Error('selector not found');e.scrollIntoView({block:'center'});const r=e.getBoundingClientRect();return {x:r.x+scrollX,y:r.y+scrollY,width:r.width,height:r.height,scale:1}})()"
		var box struct {
			Result struct {
				Value map[string]any `json:"value"`
			} `json:"result"`
		}
		if err := call("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &box); err != nil {
			return nil, err
		}
		if box.Result.Value == nil {
			return nil, agentstate.Failure("selector_not_found", "screenshot target is unavailable", coreNext("page", tab.Handle))
		}
		params["clip"] = box.Result.Value
		params["captureBeyondViewport"] = true
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := call("Page.captureScreenshot", params, &shot); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil || len(data) == 0 {
		return nil, coreArgument("browser returned an empty image")
	}
	asset, err := coreArtifact(path, "."+format, data)
	if err != nil {
		return nil, err
	}
	w, h := imageDimensions(format, data)
	asset["dimensions"] = map[string]int{"width": w, "height": h}
	asset["tab"] = tab.Handle
	asset["viewport"] = tab.Viewport
	return asset, nil
}
func coreShotURLs(command *cobra.Command, urls []string, path string, options coreShotOptions) error {
	if len(urls) == 0 || len(urls) > 100 {
		return coreArgument("provide 1–100 URLs")
	}
	for _, u := range urls {
		if !coreSafeURL(u) {
			return coreArgument("screenshot URLs must be HTTP(S) without credentials")
		}
	}
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	ctx, cancel := coreTimeout(command, 5*time.Minute)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, nil)
	if err != nil {
		return err
	}
	count := 4
	if len(urls) < count {
		count = len(urls)
	}
	if path == "" {
		path = filepath.Join(selected.DataDir, "shots", agentstate.Token("sweep-"))
	}
	results := make([]map[string]any, len(urls))
	jobs := make(chan int)
	var group sync.WaitGroup
	for i := 0; i < count; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			var created struct {
				TabID int `json:"tab_id"`
			}
			if e := client.Call(ctx, "browser.create", map[string]any{"url": "about:blank", "active": false}, &created); e != nil {
				for index := range jobs {
					results[index] = map[string]any{"error": "tab_create_failed"}
				}
				return
			}
			tab, e := store.Register(browser, created.TabID, selected.Identity(), selected.SessionKey, true)
			if e != nil {
				cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
				var ignored any
				_ = client.Call(cleanup, "browser.close", map[string]any{"tab_id": created.TabID}, &ignored)
				done()
				for index := range jobs {
					results[index] = map[string]any{"error": "tab_register_failed"}
				}
				return
			}
			tab, lease, e := store.Reserve(browser, selected.Identity(), selected.SessionKey, tab.Handle, true, true, 5*time.Minute)
			if e != nil {
				for index := range jobs {
					results[index] = map[string]any{"error": "tab_busy"}
				}
				return
			}
			defer store.Release(tab.Handle, lease)
			defer func() {
				cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
				defer done()
				var ignored any
				if client.Call(cleanup, "browser.close", map[string]any{"tab_id": tab.ChromeID}, &ignored) == nil {
					_ = store.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.Closed = true; return nil })
				}
			}()
			var ignored any
			if e = client.Call(ctx, "browser.attach", map[string]any{"tab_id": tab.ChromeID}, &ignored); e != nil {
				for index := range jobs {
					results[index] = map[string]any{"error": "attach_failed"}
				}
				return
			}
			call := func(method string, params, out any) error {
				return browserrpc.CDP(ctx, client, tab.ChromeID, method, params, out)
			}
			tab.Viewport = options.Viewport
			if options.Viewport != "" {
				w, h, _ := coreViewport(options.Viewport)
				e = call("Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": h, "deviceScaleFactor": 1, "mobile": options.Viewport == "mobile"}, &ignored)
			}
			for index := range jobs {
				if e != nil {
					results[index] = map[string]any{"error": "viewport_failed"}
					continue
				}
				navErr := client.Call(ctx, "browser.navigate", map[string]any{"tab_id": tab.ChromeID, "url": urls[index], "wait_until": "load", "timeout_ms": 15000}, &ignored)
				if navErr != nil {
					results[index] = map[string]any{"error": "navigation_failed"}
					continue
				}
				destination := filepath.Join(path, fmt.Sprintf("%03d.png", index+1))
				if len(urls) == 1 && options.URLs == "" && filepath.Ext(path) != "" {
					destination = path
				}
				asset, shotErr := coreTakeShot(call, store, tab, options, destination)
				if shotErr != nil {
					results[index] = map[string]any{"error": "screenshot_failed"}
					continue
				}
				results[index] = asset
			}
		}()
	}
	for index := range urls {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	complete := true
	for _, r := range results {
		if r["error"] != nil {
			complete = false
		}
	}
	result := coreResult{Browser: browser.Name, Profile: browser.Profile, Status: "saved", Complete: boolPointer(complete), Meta: map[string]any{"images": results, "workers": count}, Untrusted: map[string]any{"urls": urls}}
	if !complete {
		result.Status = "partial"
		result.Error = &coreError{Code: "screenshot_failed", Message: "one or more screenshots failed; inspect image outcomes", Next: coreNext("tabs"), Retry: "needs-agent"}
		if err := emitCore(command, result); err != nil {
			return err
		}
		return output.MarkReported(output.NewAgentError("screenshot_failed", command.CommandPath(), "one or more screenshots failed"))
	}
	return emitCore(command, result)
}

func coreSafeURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}
