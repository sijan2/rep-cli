package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/store"
)

func loadLiveExport(livePath string) (store.Export, error) {
	var export store.Export
	data, err := os.ReadFile(livePath)
	if err != nil {
		return export, err
	}
	if err := sonic.Unmarshal(data, &export); err != nil {
		return export, err
	}
	return export, nil
}

func maxRequestTimestamp(requests []store.Request) int64 {
	var max int64
	for _, req := range requests {
		if req.Timestamp > max {
			max = req.Timestamp
		}
	}
	return max
}

func parseSince(value string) (int64, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, nil
	}
	if isDigits(text) {
		val, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid since value: %w", err)
		}
		if len(text) <= 10 {
			return val * 1000, nil
		}
		return val, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return t.UnixMilli(), nil
	}
	t, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return 0, fmt.Errorf("invalid since value: %w", err)
	}
	return t.UnixMilli(), nil
}

func isDigits(text string) bool {
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return text != ""
}
