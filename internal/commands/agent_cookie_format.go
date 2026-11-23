package commands

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const cookieMetadataPrefix = "# Rep-Cookie-Metadata: "

// Netscape readers ignore this comment. Rep associates it with the next row,
// using the row's identity rather than allowing metadata to overwrite fields.
type cookieMetadata struct {
	Identity       []string       `json:"identity"`
	Attributes     map[string]any `json:"attributes,omitempty"`
	ExpiresSeconds string         `json:"expires_seconds,omitempty"`
	ExpiresExact   *float64       `json:"expires_exact,omitempty"`
}

type cookieInputInfo struct {
	Format          string
	IgnoredMetadata int
}

var cookieExtraAttributes = []string{"sameSite", "priority", "sameParty", "sourceScheme", "sourcePort", "partitionKey", "partitionKeyOpaque"}

func cookieRowIdentity(fields []string) []string {
	return []string{strings.TrimPrefix(fields[0], "#HttpOnly_"), strings.ToUpper(fields[1]), fields[2], fields[5]}
}

func cookieMetadataMatches(fields []string, metadata cookieMetadata) bool {
	identity := cookieRowIdentity(fields)
	if len(identity) != len(metadata.Identity) {
		return false
	}
	for i := range identity {
		if identity[i] != metadata.Identity[i] {
			return false
		}
	}
	return true
}

func cookieInputRows(source string) ([]any, cookieInputInfo, error) {
	source = strings.TrimPrefix(source, "\ufeff")
	trimmed := strings.TrimSpace(source)
	info := cookieInputInfo{Format: "json"}
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		var input any
		if json.Unmarshal([]byte(source), &input) != nil {
			return nil, info, coreArgument("cookie JSON must be an array or an object with a cookies array")
		}
		if object, ok := input.(map[string]any); ok {
			input = object["cookies"]
		}
		rows, ok := input.([]any)
		if !ok {
			return nil, info, coreArgument("cookie JSON requires a cookies array")
		}
		return rows, info, nil
	}
	info.Format = "netscape"
	rows := []any{}
	var pending *cookieMetadata
	for n, line := range strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fail := func(message string) ([]any, cookieInputInfo, error) {
			return nil, info, coreArgument(fmt.Sprintf("cookie file line %d: %s", n+1, message))
		}
		if strings.HasPrefix(line, cookieMetadataPrefix) {
			if pending != nil {
				info.IgnoredMetadata++
			}
			var metadata cookieMetadata
			if json.Unmarshal([]byte(strings.TrimPrefix(line, cookieMetadataPrefix)), &metadata) != nil {
				return fail("invalid Rep metadata comment")
			}
			pending = &metadata
			continue
		}
		if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			return fail("Netscape rows require seven tab-separated fields")
		}
		domain := strings.TrimPrefix(fields[0], "#HttpOnly_")
		if domain == "" {
			return fail("cookie domain is empty")
		}
		boolField := func(value string) (bool, bool) {
			switch strings.ToUpper(value) {
			case "TRUE":
				return true, true
			case "FALSE":
				return false, true
			}
			return false, false
		}
		subdomains, validDomain := boolField(fields[1])
		secure, validSecure := boolField(fields[3])
		if !validDomain || !validSecure {
			return fail("include-subdomains and secure fields must be TRUE or FALSE")
		}
		expires, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil || expires < 0 {
			return fail("expiry must be a nonnegative Unix timestamp (0 for session cookies)")
		}
		cookie := map[string]any{"domain": domain, "hostOnly": !subdomains, "path": fields[2], "secure": secure,
			"expires": float64(expires), "session": expires == 0, "name": fields[5], "value": fields[6],
			"httpOnly": strings.HasPrefix(fields[0], "#HttpOnly_")}
		if pending != nil {
			if cookieMetadataMatches(fields, *pending) {
				for _, key := range cookieExtraAttributes {
					if value, ok := pending.Attributes[key]; ok {
						cookie[key] = value
					}
				}
				// Keep subsecond browser expiry only while the user's row is
				// unchanged. Edited Netscape expiry always takes precedence.
				if pending.ExpiresSeconds == fields[4] && pending.ExpiresExact != nil && expires > 0 &&
					math.Floor(*pending.ExpiresExact) == float64(expires) {
					cookie["expires"] = *pending.ExpiresExact
				}
			} else {
				info.IgnoredMetadata++
			}
			pending = nil
		}
		rows = append(rows, cookie)
	}
	if pending != nil {
		info.IgnoredMetadata++
	}
	return rows, info, nil
}

func encodeCookieExport(cookies []map[string]any, format string) ([]byte, map[string]any, error) {
	meta := map[string]any{"format": format}
	if format == "json" {
		data, err := json.MarshalIndent(map[string]any{"cookies": cookies}, "", "  ")
		return append(data, '\n'), meta, err
	}
	if format != "netscape" {
		return nil, nil, coreArgument("cookie export format must be json or netscape")
	}
	var out strings.Builder
	out.WriteString("# Netscape HTTP Cookie File\n# Rep metadata comments preserve browser-only attributes when re-imported by rep.\n# Other consumers ignore these attributes and do not enforce cookie partitioning.\n")
	omitted := map[string]bool{}
	for i, cookie := range cookies {
		name, nameOK := cookie["name"].(string)
		value, valueOK := cookie["value"].(string)
		domain, domainOK := cookie["domain"].(string)
		path, pathOK := cookie["path"].(string)
		if !nameOK || !valueOK || !domainOK || !pathOK || strings.ContainsAny(name+value+domain+path, "\t\r\n\x00") {
			return nil, nil, coreArgument(fmt.Sprintf("cookie %d cannot be represented by a Netscape row; use --format json", i+1))
		}
		boolString := func(value bool) string {
			if value {
				return "TRUE"
			}
			return "FALSE"
		}
		expires, _ := cookie["expires"].(float64)
		expiry := "0"
		if cookie["session"] != true && expires > 0 {
			expiry = strconv.FormatInt(int64(math.Floor(expires)), 10)
		}
		hostOnly, explicit := cookie["hostOnly"].(bool)
		if !explicit {
			hostOnly = !strings.HasPrefix(domain, ".")
		}
		fields := []string{domain, boolString(!hostOnly), path, boolString(cookie["secure"] == true), expiry, name, value}
		metadata := cookieMetadata{Identity: cookieRowIdentity(fields), Attributes: map[string]any{}}
		for _, key := range cookieExtraAttributes {
			if value, ok := cookie[key]; ok {
				metadata.Attributes[key] = value
				omitted[key] = true
			}
		}
		if expiry != "0" && math.Floor(expires) != expires {
			metadata.ExpiresSeconds, metadata.ExpiresExact = expiry, &expires
			omitted["subsecond_expiry"] = true
		}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return nil, nil, err
		}
		out.WriteString(cookieMetadataPrefix + string(encoded) + "\n")
		if cookie["httpOnly"] == true {
			fields[0] = "#HttpOnly_" + fields[0]
		}
		out.WriteString(strings.Join(fields, "\t") + "\n")
	}
	attrs := make([]string, 0, len(omitted))
	for key := range omitted {
		attrs = append(attrs, key)
	}
	sort.Strings(attrs)
	meta["rep_metadata_comments"] = true
	meta["external_consumers_ignore"] = attrs
	return []byte(out.String()), meta, nil
}

// A host-only cookie belongs only to its exact host; a domain cookie also
// applies to descendants. Path and scheme do not narrow an all-paths export.
func cookiesForHost(cookies []map[string]any, host string) []map[string]any {
	selected := []map[string]any{}
	for _, cookie := range cookies {
		domain, _ := cookie["domain"].(string)
		if cookieHostName(domain) == cookieHostName(host) || strings.HasPrefix(domain, ".") && cookieDomainMatches(host, domain) {
			selected = append(selected, cookie)
		}
	}
	return selected
}
