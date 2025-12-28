package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

// HeaderMap preserves multi-value headers while remaining JSON-friendly.
type HeaderMap map[string][]string

// HeaderKV supports legacy array-of-objects header formats.
type HeaderKV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// UnmarshalJSON accepts map[string]string, map[string][]string, or []HeaderKV.
func (h *HeaderMap) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*h = nil
		return nil
	}

	var multi map[string][]string
	if err := json.Unmarshal(data, &multi); err == nil {
		*h = HeaderMap(multi)
		return nil
	}

	var single map[string]string
	if err := json.Unmarshal(data, &single); err == nil {
		converted := make(map[string][]string, len(single))
		for k, v := range single {
			converted[k] = []string{v}
		}
		*h = HeaderMap(converted)
		return nil
	}

	var list []HeaderKV
	if err := json.Unmarshal(data, &list); err == nil {
		converted := make(map[string][]string)
		for _, item := range list {
			if item.Name == "" {
				continue
			}
			converted[item.Name] = append(converted[item.Name], item.Value)
		}
		*h = HeaderMap(converted)
		return nil
	}

	return fmt.Errorf("unsupported header format")
}

// HeaderValues returns the values for a header name (case-insensitive).
func HeaderValues(headers HeaderMap, name string) []string {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return values
		}
	}
	return nil
}

// HeaderValuesWithKey returns the canonical key and values for a header name.
func HeaderValuesWithKey(headers HeaderMap, name string) (string, []string) {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return key, values
		}
	}
	return "", nil
}

// HeaderFirst returns the first header value for a name (case-insensitive).
func HeaderFirst(headers HeaderMap, name string) string {
	values := HeaderValues(headers, name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
