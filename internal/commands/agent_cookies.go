package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func newCoreCookiesCommand() *cobra.Command {
	group := &cobra.Command{Use: "cookies COMMAND", Short: "Inspect, export and restore browser-session cookies", Long: "Includes HttpOnly cookies applicable to the addressed tab URL. --all-paths includes every cookie path for that host, including parent-domain cookies and Secure cookies. JSON preserves native fields; Netscape files interoperate with curl and other cookie-jar tools. Cookies belong to the browser profile, shared by its tabs; use REP_PROFILE for independent logins."}
	group.Example = "  rep cookies list t3 --all-paths\n  rep cookies export t3 session.json --all-paths\n  rep cookies export t3 cookies.txt --format netscape --all-paths\n  rep cookies import t3 @cookies.txt\n  rep cookies delete t3 session-id"
	for _, action := range []string{"list", "export", "import", "delete"} {
		action := action
		use, short, count := "list TAB", "List cookie metadata and fingerprinted values", 1
		switch action {
		case "export":
			use, short, count = "export TAB PATH", "Save raw cookies as JSON or a Netscape cookie jar", 2
		case "import":
			use, short, count = "import TAB @FILE", "Restore JSON or Netscape cookies (@- for stdin)", 2
		case "delete":
			use, short, count = "delete TAB NAME", "Delete a named cookie at this URL (optional domain/path selector)", 2
		}
		var domain, path, partition string
		options := cookieOptions{Format: "json"}
		command := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(count)}
		switch action {
		case "list":
			command.Example = "  rep cookies list t3\n  rep cookies list t3 --all-paths"
		case "export":
			command.Example = "  rep cookies export t3 session.json --all-paths\n  rep cookies export t3 cookies.txt --format netscape --all-paths"
			command.Long = "JSON keeps native cookie fields. Netscape rows support curl/yt-dlp-style jars, with Rep comments retaining SameSite, partition and other browser attributes for re-import. Ordinary Netscape consumers ignore those comments and do not enforce partitioning. --all-paths collects all paths/schemes for the tab host, including applicable parent-domain cookies."
		case "import":
			command.Example = "  rep cookies import t3 @session.json\n  rep cookies import t3 @cookies.txt --all-domains\n  cat cookies.txt | rep cookies import t3 @-"
			command.Long = "Auto-detects an exported {\"cookies\":[...]} object, JSON array or Netscape cookie jar, up to 8 MiB. Preserves HttpOnly, Secure, path and expiry; JSON and Rep Netscape comments also preserve SameSite and partition keys. Common browser-export hostOnly and expirationDate fields are accepted. By default rows must match the tab host; --all-domains restores every declared domain into the selected browser profile. Input is validated before submission; an empty jar is a no-op."
		case "delete":
			command.Example = "  rep cookies delete t3 session-id\n  rep cookies delete t3 session-id --domain .example.com --path /"
		}
		if action == "delete" {
			command.Flags().StringVar(&domain, "domain", "", "Exact cookie domain from list; defaults to tab URL")
			command.Flags().StringVar(&path, "path", "", "Exact cookie path from list")
			command.Flags().StringVar(&partition, "partition-key", "", "Partition key JSON from list for a partitioned cookie")
		}
		if action == "list" || action == "export" {
			command.Flags().BoolVar(&options.AllPaths, "all-paths", false, "Include all paths/schemes for this host and applicable parent domains")
		}
		if action == "export" {
			command.Flags().StringVar(&options.Format, "format", "json", "Export format: json or netscape")
		}
		if action == "import" {
			command.Flags().BoolVar(&options.AllDomains, "all-domains", false, "Restore every declared domain into the selected profile")
		}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			if err := coreCookies(cmd, action, args, domain, path, partition, options); err != nil {
				return coreFailure(cmd, err)
			}
			return nil
		}
		group.AddCommand(command)
	}
	return group
}

func cookieSource(command *cobra.Command, argument string) (string, error) {
	if !strings.HasPrefix(argument, "@") {
		return "", coreArgument("use @FILE or @- for JSON or Netscape cookies")
	}
	var reader io.Reader = command.InOrStdin()
	if argument != "@-" {
		file, err := os.Open(strings.TrimPrefix(argument, "@"))
		if err != nil {
			return "", coreArgument("cookie source file is unreadable")
		}
		defer file.Close()
		reader = file
	}
	const limit = 8 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", err
	}
	if len(data) > limit {
		return "", coreArgument("cookie source exceeds 8 MiB")
	}
	return string(data), nil
}

func cookieDomainMatches(host, domain string) bool {
	domain = cookieHostName(domain)
	host = cookieHostName(host)
	return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

func cookieHostName(host string) string {
	return strings.Trim(strings.TrimPrefix(strings.ToLower(host), "."), "[]")
}

func cookieDomainURL(domain string, cookie map[string]any) string {
	scheme := "http"
	if cookie["secure"] == true || cookie["sourceScheme"] == "Secure" {
		scheme = "https"
	}
	host := strings.TrimPrefix(domain, ".")
	if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/"}).String()
}

func cookieListItems(cookies []map[string]any) []map[string]any {
	items := make([]map[string]any, 0, len(cookies))
	for _, cookie := range cookies {
		item := make(map[string]any, len(cookie))
		for key, value := range cookie {
			if key == "session" {
				// The generic redactor treats "session" as a credential field.
				// This is a boolean attribute, so name it explicitly in listings.
				key = "sessionOnly"
			}
			item[key] = value
		}
		item["value"] = output.Fingerprint(fmt.Sprint(cookie["value"]))
		items = append(items, item)
	}
	return items
}

func validateCookieFields(cookie map[string]any) error {
	name, nameOK := cookie["name"].(string)
	value, valueOK := cookie["value"].(string)
	if !nameOK || !valueOK || name != "" && !coreHTTPToken.MatchString(name) || strings.ContainsAny(value, "\r\n\x00;") {
		return fmt.Errorf("name and value must be valid cookie strings")
	}
	for _, key := range []string{"secure", "httpOnly", "sameParty", "hostOnly", "session", "partitionKeyOpaque"} {
		if value, exists := cookie[key]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", key)
			}
		}
	}
	if cookie["partitionKeyOpaque"] == true {
		return fmt.Errorf("opaque partition keys cannot be restored")
	}
	for _, key := range []string{"expires", "expirationDate", "sourcePort"} {
		if value, exists := cookie[key]; exists {
			number, ok := value.(float64)
			if !ok || number < -1 {
				return fmt.Errorf("%s must be a number greater than or equal to -1", key)
			}
			if key == "sourcePort" && (number > 65535 || (number != -1 && number < 1) || number != float64(int(number))) {
				return fmt.Errorf("sourcePort must be -1 or an integer port from 1 to 65535")
			}
		}
	}
	if value, exists := cookie["path"]; exists {
		path, ok := value.(string)
		if !ok || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n\x00;") {
			return fmt.Errorf("path must be an absolute cookie path")
		}
	}
	for key, options := range map[string]string{"priority": "Low Medium High", "sourceScheme": "Unset NonSecure Secure"} {
		if value, exists := cookie[key]; exists {
			text, ok := value.(string)
			if !ok || text == "" || !strings.Contains(" "+options+" ", " "+text+" ") {
				return fmt.Errorf("%s has an unsupported value", key)
			}
		}
	}
	if value, exists := cookie["partitionKey"]; exists {
		return validateCookiePartition(value)
	}
	return nil
}

func validateCookiePartition(value any) error {
	key, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("partitionKey must be an object containing topLevelSite")
	}
	site, ok := key["topLevelSite"].(string)
	if !ok {
		return fmt.Errorf("partitionKey.topLevelSite must be an HTTP(S) site")
	}
	if _, err := coreRequestURL(site); err != nil {
		return fmt.Errorf("partitionKey.topLevelSite must be an HTTP(S) site")
	}
	if value, exists := key["hasCrossSiteAncestor"]; exists {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("partitionKey.hasCrossSiteAncestor must be a boolean")
		}
	}
	return nil
}

// Convert Network.Cookie output (and common browser-export fields) to CookieParam.
// Validate the entire batch before writing any cookie. Keep host-only cookies
// host-only by supplying a URL rather than converting them to domain cookies.
func cookieImportParams(source, pageURL string) ([]map[string]any, error) {
	params, _, err := cookieImportData(source, pageURL)
	return params, err
}

func cookieImportData(source, pageURL string, allDomains ...bool) ([]map[string]any, cookieInputInfo, error) {
	rows, info, err := cookieInputRows(source)
	if err != nil {
		return nil, info, err
	}
	params, err := cookieRowsParams(rows, pageURL, len(allDomains) > 0 && allDomains[0])
	return params, info, err
}

func cookieRowsParams(rows []any, pageURL string, allDomains bool) ([]map[string]any, error) {
	if _, err := coreRequestURL(pageURL); err != nil {
		return nil, err
	}
	page, _ := url.Parse(pageURL)
	result := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		fail := func(message string) ([]map[string]any, error) {
			return nil, coreArgument(fmt.Sprintf("cookie %d: %s", i+1, message))
		}
		cookie, ok := row.(map[string]any)
		if !ok {
			return fail("expected an object")
		}
		_, nameOK := cookie["name"].(string)
		_, valueOK := cookie["value"].(string)
		if !nameOK || !valueOK {
			return fail("name and value must be strings")
		}
		if err := validateCookieFields(cookie); err != nil {
			return fail(err.Error())
		}
		params := map[string]any{}
		for _, key := range []string{"name", "value", "url", "domain", "path", "secure", "httpOnly", "sameSite", "expires", "priority", "sameParty", "sourceScheme", "sourcePort", "partitionKey"} {
			if value, exists := cookie[key]; exists {
				params[key] = value
			}
		}
		cookieURL := pageURL
		if raw, exists := params["url"]; exists {
			address, ok := raw.(string)
			parsed, err := url.Parse(address)
			if !ok || err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
				return fail("url must be an absolute HTTP(S) URL without credentials")
			}
			if !allDomains && !strings.EqualFold(parsed.Hostname(), page.Hostname()) {
				return fail("url must address the tab's host; use --all-domains for a multi-site jar")
			}
			cookieURL = address
		}
		if raw, exists := params["domain"]; exists {
			domain, ok := raw.(string)
			ip := net.ParseIP(cookieHostName(domain))
			if !ok || cookieHostName(domain) == "" || strings.ContainsAny(domain, "/\\?#@ \t\r\n\x00") || ip == nil && strings.ContainsAny(domain, ":[]") {
				return fail("domain must be a hostname")
			}
			if !allDomains && !cookieDomainMatches(page.Hostname(), domain) {
				return fail("domain must match the tab's host; use --all-domains for a multi-site jar")
			}
			if _, explicitURL := params["url"]; !explicitURL && allDomains && !cookieDomainMatches(page.Hostname(), domain) {
				cookieURL = cookieDomainURL(domain, cookie)
			}
			cookieAddress, _ := url.Parse(cookieURL)
			if !cookieDomainMatches(cookieAddress.Hostname(), domain) {
				return fail("domain does not match the cookie's URL")
			}
			hostOnly, explicitHostOnly := cookie["hostOnly"].(bool)
			if hostOnly || (!explicitHostOnly && !strings.HasPrefix(domain, ".")) {
				if cookieHostName(domain) != cookieHostName(cookieAddress.Hostname()) {
					if allDomains && params["url"] == nil {
						cookieURL = cookieDomainURL(domain, cookie)
					} else {
						return fail("host-only domain must equal its cookie URL host; use --all-domains for other hosts")
					}
				}
				delete(params, "domain")
			} else if !strings.HasPrefix(domain, ".") {
				params["domain"] = "." + domain
			}
		}
		if _, explicitURL := params["url"]; !explicitURL {
			// Native exports can describe a cookie established at a different
			// path, scheme or source port than the currently displayed page.
			// Chromium rejects conflicting URL/source fields during restoration.
			address, _ := url.Parse(cookieURL)
			if params["sourceScheme"] == "Secure" || params["sourceScheme"] == nil && params["secure"] == true {
				address.Scheme = "https"
			} else if params["sourceScheme"] == "NonSecure" {
				address.Scheme = "http"
			}
			if path, ok := params["path"].(string); ok {
				address.Path, address.RawPath = path, ""
			}
			if port, ok := params["sourcePort"].(float64); ok && port > 0 {
				address.Host = net.JoinHostPort(address.Hostname(), strconv.Itoa(int(port)))
			}
			address.RawQuery, address.Fragment, address.RawFragment = "", "", ""
			cookieURL = address.String()
		}
		params["url"] = cookieURL
		if _, ok := params["expires"]; !ok {
			if expiry, ok := cookie["expirationDate"]; ok {
				params["expires"] = expiry
			}
		}
		if cookie["session"] == true || params["expires"] == float64(-1) {
			delete(params, "expires")
		}
		if value, exists := params["sameSite"]; exists {
			sameSite, ok := value.(string)
			if !ok {
				return fail("sameSite must be a string")
			}
			switch strings.ToLower(sameSite) {
			case "unspecified", "":
				delete(params, "sameSite")
			case "no_restriction", "none":
				params["sameSite"] = "None"
			case "lax":
				params["sameSite"] = "Lax"
			case "strict":
				params["sameSite"] = "Strict"
			default:
				return fail("sameSite must be Strict, Lax, None or unspecified")
			}
		}
		result = append(result, params)
	}
	return result, nil
}

type cookieOptions struct {
	AllPaths   bool
	AllDomains bool
	Format     string
}

func coreCookies(command *cobra.Command, action string, args []string, domain, path, partition string, options cookieOptions) error {
	if options.Format == "" {
		options.Format = "json"
	}
	if options.Format != "json" && options.Format != "netscape" {
		return coreArgument("cookie export format must be json or netscape")
	}
	if action == "export" && strings.TrimSpace(args[1]) == "" {
		return coreArgument("cookies export requires a new file path")
	}
	if !agentstate.TabPattern.MatchString(args[0]) {
		return coreArgument("cookies requires an explicit tab handle; use rep page URL first")
	}
	index, err := coreStore()
	if err != nil {
		return err
	}
	addressed, err := index.Tab(args[0])
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	if addressed.Owner != selected.Identity() {
		return agentstate.Failure("owned_by_other_task", "explicit task does not own this tab", coreNext("scope"))
	}
	ctx, cancel := coreTimeout(command, 30*time.Second)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, index, addressed)
	if err != nil {
		return err
	}
	tab, lease, err := index.Reserve(browser, selected.Identity(), selected.SessionKey, addressed.Handle, selected.TaskSource == "flag", false, 32*time.Second)
	if err != nil {
		return err
	}
	defer index.Release(tab.Handle, lease)
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: tab.Owner}
	defer session.Close()
	if err = session.Acquire(ctx); err != nil {
		return err
	}
	var location struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err = session.Inspect(ctx, "", "Runtime.evaluate", map[string]any{"expression": "location.href", "returnByValue": true}, &location); err != nil {
		return err
	}
	pageURL := location.Result.Value
	if _, err = coreRequestURL(pageURL); err != nil {
		return err
	}
	page, _ := url.Parse(pageURL)
	result := coreTabResult(tab)
	meta := map[string]any{"operation": "cookies." + action, "scope": "cookies applicable to tab URL; profile shared by its tabs"}
	if options.AllPaths {
		meta["scope"] = "all paths and schemes for tab host, including applicable parent-domain cookies; profile shared by its tabs"
	}
	if options.AllDomains && action == "import" {
		meta["scope"] = "every declared cookie domain in the selected browser profile; shared by its tabs"
	}
	result.Meta = meta
	if err = index.Journal(tab.Owner, tab.Handle, "cookies."+action, "requested"); err != nil {
		return err
	}
	outcome := "failed"
	defer func() { _ = index.Journal(tab.Owner, tab.Handle, "cookies."+action, outcome) }()
	var wire struct {
		Cookies []map[string]any `json:"cookies"`
	}
	switch action {
	case "list", "export":
		method, params := "Network.getCookies", map[string]any{"urls": []string{pageURL}}
		if options.AllPaths {
			method, params = "Storage.getCookies", map[string]any{}
		}
		if err = session.Inspect(ctx, pageURL, method, params, &wire); err != nil {
			return err
		}
		if options.AllPaths {
			wire.Cookies = cookiesForHost(wire.Cookies, page.Hostname())
		}
		if wire.Cookies == nil {
			wire.Cookies = []map[string]any{}
		}
		meta["count"] = len(wire.Cookies)
		if action == "export" {
			data, formatMeta, e := encodeCookieExport(wire.Cookies, options.Format)
			if e != nil {
				return e
			}
			artifact, e := coreArtifact(args[1], ".json", data)
			if e != nil {
				return e
			}
			meta["artifact"] = artifact
			for key, value := range formatMeta {
				meta[key] = value
			}
		} else {
			// "cookies" is a credential field in the general output redactor.
			// Use items so it preserves metadata already fingerprinted above.
			result.Untrusted = map[string]any{"url": pageURL, "items": cookieListItems(wire.Cookies)}
		}
	case "import":
		if !strings.HasPrefix(args[1], "@") {
			return coreArgument("use @FILE or @- for JSON or Netscape cookies")
		}
		source, e := cookieSource(command, args[1])
		if e != nil {
			return e
		}
		cookies, info, e := cookieImportData(source, pageURL, options.AllDomains)
		if e != nil {
			return e
		}
		outcome = "outcome_unknown"
		var ignored any
		if err = session.Inspect(ctx, pageURL, "Network.setCookies", map[string]any{"cookies": cookies}, &ignored); err != nil {
			var rejected *bridge.RPCError
			if errors.As(err, &rejected) {
				message := output.RedactBody(rejected.Message, "text/plain")
				for _, cookie := range cookies {
					if value, ok := cookie["value"].(string); ok && value != "" {
						message = strings.ReplaceAll(message, value, output.Fingerprint(value))
					}
				}
				problem := agentstate.Failure("cookie_import_rejected", "browser rejected cookie import: "+message+"; earlier cookies may have been applied, inspect before retrying", coreNext("cookies", "list", tab.Handle, "--all-paths"))
				problem.Retry = "do-not-retry"
				return problem
			}
			return err
		}
		meta["count"] = len(cookies)
		meta["format"] = info.Format
		if info.IgnoredMetadata > 0 {
			meta["ignored_metadata_comments"] = info.IgnoredMetadata
		}
		meta["verification"] = "browser acknowledged import; reload or fetch to verify authentication"
	case "delete":
		params := map[string]any{"name": args[1], "url": pageURL}
		if partition != "" {
			var key any
			if json.Unmarshal([]byte(partition), &key) != nil {
				return coreArgument("--partition-key expects JSON from cookies list")
			}
			if err := validateCookiePartition(key); err != nil {
				return coreArgument(err.Error())
			}
			params["partitionKey"] = key
		}
		if domain != "" {
			if !cookieDomainMatches(page.Hostname(), domain) {
				return coreArgument("cookie domain must match the tab's host")
			}
			params["domain"] = domain
			delete(params, "url")
		}
		if path != "" {
			params["path"] = path
		}
		outcome = "outcome_unknown"
		var ignored any
		if err = session.Inspect(ctx, pageURL, "Network.deleteCookies", params, &ignored); err != nil {
			return err
		}
		meta["verification"] = "browser acknowledged deletion; list cookies to verify"
	}
	outcome = "complete"
	result.Status, result.Complete = "complete", boolPointer(true)
	return emitCore(command, result)
}

func init() { rootCmd.AddCommand(newCoreCookiesCommand()) }
