package noise

import "strings"

// KnownNoisePatterns maps domain patterns to their noise type
// Types: analytics, tracking, ads, monitoring, cdn, social, marketing, support
var KnownNoisePatterns = map[string]string{
	"google-analytics.com":   "analytics",
	"googletagmanager.com":   "analytics",
	"analytics.google.com":   "analytics",
	"facebook.net":           "tracking",
	"facebook.com":           "tracking",
	"fbcdn.net":              "cdn",
	"doubleclick.net":        "tracking",
	"googlesyndication.com":  "ads",
	"googleadservices.com":   "ads",
	"hotjar.com":             "analytics",
	"hotjar.io":              "analytics",
	"mixpanel.com":           "analytics",
	"segment.io":             "analytics",
	"segment.com":            "analytics",
	"amplitude.com":          "analytics",
	"fullstory.com":          "analytics",
	"sentry.io":              "monitoring",
	"newrelic.com":           "monitoring",
	"datadoghq.com":          "monitoring",
	"cloudflare.com":         "cdn",
	"cloudflareinsights.com": "analytics",
	"cdn.jsdelivr.net":       "cdn",
	"cdnjs.cloudflare.com":   "cdn",
	"unpkg.com":              "cdn",
	"fonts.googleapis.com":   "cdn",
	"fonts.gstatic.com":      "cdn",
	"ajax.googleapis.com":    "cdn",
	"twitter.com":            "social",
	"linkedin.com":           "social",
	"intercom.io":            "support",
	"intercomcdn.com":        "cdn",
	"zendesk.com":            "support",
	"crisp.chat":             "support",
	"hubspot.com":            "marketing",
	"hs-scripts.com":         "marketing",
	"hsforms.com":            "marketing",
	"clarity.ms":             "analytics",
	"bing.com":               "analytics",
	"bat.bing.com":           "analytics",
}

// DetectNoiseType returns the noise type for a domain, or empty string if not noise
func DetectNoiseType(domain string) string {
	for pattern, ptype := range KnownNoisePatterns {
		if strings.Contains(domain, pattern) {
			return ptype
		}
	}
	return ""
}

// IsNoise returns true if the domain matches a known noise pattern
func IsNoise(domain string) bool {
	return DetectNoiseType(domain) != ""
}

// IsCDN returns true if the domain is identified as a CDN
func IsCDN(domain string) bool {
	return DetectNoiseType(domain) == "cdn"
}

// IsAnalytics returns true if the domain is identified as analytics
func IsAnalytics(domain string) bool {
	return DetectNoiseType(domain) == "analytics"
}

// IsTracking returns true if the domain is identified as tracking
func IsTracking(domain string) bool {
	return DetectNoiseType(domain) == "tracking"
}

// GetCDNDomains returns all known CDN domain patterns
func GetCDNDomains() []string {
	var result []string
	for pattern, ptype := range KnownNoisePatterns {
		if ptype == "cdn" {
			result = append(result, pattern)
		}
	}
	return result
}

// GetNoiseTypes returns all unique noise types
func GetNoiseTypes() []string {
	typeSet := make(map[string]bool)
	for _, ptype := range KnownNoisePatterns {
		typeSet[ptype] = true
	}
	var result []string
	for t := range typeSet {
		result = append(result, t)
	}
	return result
}
