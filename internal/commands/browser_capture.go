package commands

import (
	"encoding/json"
)

func browserBodyState(value string) string {
	switch value {
	case "complete", "partial", "unavailable", "pending", "not_applicable":
		return value
	default:
		return "unknown"
	}
}

func browserResultRequestCount(value interface{}) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, number >= 0
	case int32:
		return int(number), number >= 0
	case int64:
		return int(number), number >= 0
	case float64:
		integer := int(number)
		return integer, number >= 0 && float64(integer) == number
	case json.Number:
		integer, err := number.Int64()
		return int(integer), err == nil && integer >= 0
	default:
		return 0, false
	}
}
