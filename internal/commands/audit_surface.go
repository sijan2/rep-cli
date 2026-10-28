package commands

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/repplus/rep-cli/internal/surface"
)

// Surface-model audit. The legacy per-request scoring is retained behind
// --legacy-score for comparison; this is the default path because the old one
// was measured producing 206 rows tied at a single score on a real capture,
// 15 of its top 20 being fonts, stylesheets and script bundles.

func observationsFrom(requests []store.Request, primary map[string]bool) []surface.Observation {
	out := make([]surface.Observation, 0, len(requests))
	for i := range requests {
		r := &requests[i]
		if r.Domain == "" {
			store.ComputeRequestFields(r)
		}
		o := surface.Observation{
			ID: r.ID, Method: r.Method, URL: r.URL, Domain: r.Domain,
			ResourceType: r.ResourceType,
			HasCookie:    store.HeaderFirst(r.Headers, "cookie") != "",
			HasAuthzHdr:  store.HeaderFirst(r.Headers, "authorization") != "",
			IsPrimary:    len(primary) == 0 || primary[strings.ToLower(r.Domain)],
		}
		for name := range r.Headers {
			o.HeaderNames = append(o.HeaderNames, name)
		}
		if r.Response != nil {
			o.Status = r.Response.Status
			o.ContentType = store.HeaderFirst(r.Response.Headers, "content-type")
			o.RespBytes = len(r.Response.Body)
		}
		o.QueryKeys = queryKeysOf(r.URL)
		o.BodyKeys = bodyKeysOf(r.Body, store.HeaderFirst(r.Headers, "content-type"))
		out = append(out, o)
	}
	return out
}

func queryKeysOf(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(u.Query()))
	for k := range u.Query() {
		keys = append(keys, k)
	}
	return keys
}

// bodyKeysOf reports top-level field names only. Values never leave this
// function: scoring needs to know that a client asserted "price", not what it
// claimed the price was.
func bodyKeysOf(body, contentType string) []string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil
	}
	if strings.Contains(strings.ToLower(contentType), "x-www-form-urlencoded") {
		values, err := url.ParseQuery(trimmed)
		if err != nil {
			return nil
		}
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		return keys
	}
	if trimmed[0] != '{' {
		return nil
	}
	var root map[string]interface{}
	if err := sonic.Unmarshal([]byte(trimmed), &root); err != nil {
		return nil
	}
	keys := make([]string, 0, len(root))
	for k := range root {
		keys = append(keys, k)
	}
	return keys
}

func emitSurfaceAudit(source string, families []surface.Family, candidates []surface.Candidate, scanned int, jsonMode bool) error {
	excluded := 0
	scored := 0
	for _, f := range families {
		if f.Kind == surface.KindStatic {
			excluded++
		}
		if f.Score > 0 {
			scored++
		}
	}

	if jsonMode {
		env := output.WrapData("audit", source, map[string]interface{}{
			"requests_scanned": scanned,
			"families":         len(families),
			"families_scored":  scored,
			"families_static":  excluded,
			"candidates":       candidates,
			"termination_note": "an empty candidate list means nothing in THIS capture scored, not that the target is secure",
		})
		env.Filters = map[string]interface{}{"primary_only": auditPrimaryOnly, "top": auditTop}
		if len(candidates) > 0 {
			env.Suggest = []string{
				candidates[0].Next,
				"rep probe add --hypothesis '<h>' --oracle " + string(candidates[0].Oracle) + " --ref " + candidates[0].RefID,
			}
		}
		buf, err := sonic.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}

	fmt.Printf("%s\n", output.FormatSourceLine(source))
	fmt.Printf("%d requests -> %d endpoint families (%d static excluded, %d scored)\n\n",
		scanned, len(families), excluded, scored)
	if len(candidates) == 0 {
		fmt.Println("no candidates scored in this capture.")
		fmt.Println("this is not evidence the target is secure - capture authenticated traffic and retry.")
		return nil
	}
	for i, c := range candidates {
		names := make([]string, 0, len(c.Signals))
		for _, s := range c.Signals {
			if s.Weight > 0 {
				names = append(names, s.Name)
			}
		}
		fmt.Printf("%2d. [%5.1f] %-6s %s%s\n", i+1, c.Score, c.Method, c.Host, truncateRoute(c.Route, 64))
		fmt.Printf("      oracle: %s\n", c.Oracle)
		fmt.Printf("      why:    %s\n", c.Hypothesis)
		fmt.Printf("      signals:%s\n", " "+strings.Join(names, ","))
		fmt.Printf("      next:   %s\n\n", c.Next)
	}
	return nil
}

func truncateRoute(route string, max int) string {
	if len(route) <= max {
		return route
	}
	return route[:max-3] + "..."
}

func emitSurfaceEmpty(jsonMode bool) error {
	if jsonMode {
		env := output.WrapData("audit", "live.json", map[string]interface{}{
			"requests_scanned": 0, "candidates": []surface.Candidate{},
			"termination_note": "no capture in this task",
		})
		buf, err := sonic.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Fprintln(os.Stdout, "no capture in this task")
	return nil
}
