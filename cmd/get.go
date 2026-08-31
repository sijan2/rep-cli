package cmd

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/spf13/cobra"
	"github.com/repplus/rep-cli/internal/store"
)

var getCmd = &cobra.Command{
	Use:   "get <id> [--field <path>]",
	Short: "Extract field from request by ID",
	Long: `Extract specific fields from a request using dot notation.

Examples:
  rep get h_abc123 --field req_headers.authorization
  rep get h_abc123 --field req_headers.cookie
  rep get h_abc123 --field res_headers.set-cookie
  rep get h_abc123                                    # full JSON`,
	Args: cobra.ExactArgs(1),
	Run:  runGet,
}

var getField string

func init() {
	getCmd.Flags().StringVarP(&getField, "field", "f", "", "Field path (dot notation)")
	rootCmd.AddCommand(getCmd)
}

func runGet(cmd *cobra.Command, args []string) {
	id := args[0]

	// Try Android first
	if req := findAndroidRequest(id); req != nil {
		outputField(req, getField)
		return
	}

	// Try web
	if req := findWebRequest(id); req != nil {
		outputField(req, getField)
		return
	}

	fmt.Println("Request not found:", id)
}

func findAndroidRequest(id string) map[string]interface{} {
	data, err := store.LoadAndroidData()
	if err != nil {
		return nil
	}
	for _, pkg := range data.Packages {
		for _, r := range pkg.Requests {
			if r.ID == id {
				b, _ := sonic.Marshal(r)
				var m map[string]interface{}
				sonic.Unmarshal(b, &m)
				return m
			}
		}
	}
	return nil
}

func findWebRequest(id string) map[string]interface{} {
	livePath, err := store.GetLiveFilePath()
	if err != nil {
		return nil
	}
	export, err := loadLiveExport(livePath)
	if err != nil {
		return nil
	}
	for _, r := range export.Requests {
		if r.ID == id || string(r.SemanticID) == id {
			b, _ := sonic.Marshal(r)
			var m map[string]interface{}
			sonic.Unmarshal(b, &m)
			return m
		}
	}
	return nil
}

func outputField(data map[string]interface{}, field string) {
	if field == "" {
		b, _ := sonic.MarshalIndent(data, "", "  ")
		fmt.Println(string(b))
		return
	}

	val := extractField(data, field)
	if val == nil {
		return
	}

	switch v := val.(type) {
	case string:
		fmt.Println(v)
	case []interface{}:
		for _, item := range v {
			fmt.Println(item)
		}
	default:
		b, _ := sonic.Marshal(v)
		fmt.Println(string(b))
	}
}

func extractField(data map[string]interface{}, path string) interface{} {
	parts := strings.Split(strings.ToLower(path), ".")
	var current interface{} = data

	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			// Case-insensitive lookup
			found := false
			for k, val := range v {
				if strings.EqualFold(k, part) {
					current = val
					found = true
					break
				}
			}
			if !found {
				return nil
			}
		default:
			return nil
		}
	}
	return current
}
