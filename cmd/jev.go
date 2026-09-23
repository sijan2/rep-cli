package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var jevCmd = &cobra.Command{
	Use:   "jev",
	Short: "Typed Jev decisions for browser and captured-traffic organization",
	Long:  "Use TypeSafe Jev for bounded structured decisions. Credentials stay in the local CLI/native host. Jev produces typed decisions; browser select and browser interact use them within explicit browser workflows.",
}

func init() {
	rootCmd.AddCommand(jevCmd)
	var envFile string
	configCommand := &cobra.Command{
		Use: "config --env-file PATH", Short: "Remember a local env file for both CLI and native host", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := jev.SetEnvFile(envFile)
			if err != nil {
				return reportJevError(cmd, err)
			}
			return emitJev(cmd, "jev config", map[string]any{"configured": true, "env_file": path, "stores_key": false})
		},
	}
	configCommand.Flags().StringVar(&envFile, "env-file", "", "Path to an env file with JEV, JEV_API_KEY, or TYPESAFE_API_KEY")
	_ = configCommand.MarkFlagRequired("env-file")
	jevCmd.AddCommand(configCommand)
	jevCmd.AddCommand(&cobra.Command{
		Use: "status", Short: "Check local Jev configuration without an API call", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := jev.LoadConfig()
			result := map[string]any{"configured": err == nil, "model": jev.Model, "endpoint": jev.Endpoint}
			if err != nil {
				result["error"] = err.Error()
			} else {
				result["source"] = config.Source
				result["model"] = config.Model
			}
			return emitJev(cmd, "jev status", result)
		},
	})
	jevCmd.AddCommand(&cobra.Command{
		Use: "doctor", Aliases: []string{"smoke"}, Short: "Verify Jev with synthetic traffic metadata (one API evaluation)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := jev.LoadConfig()
			if err != nil {
				return reportJevError(cmd, err)
			}
			result, err := jev.NewClient(config).Classify(cmd.Context(), jev.TrafficInput{Method: "GET", URL: "https://example.com/assets/app.js", ResourceType: "script", Status: 200, ContentType: "application/javascript"})
			if err != nil {
				return reportJevError(cmd, err)
			}
			return emitJev(cmd, "jev doctor", map[string]any{"verified": true, "synthetic": true, "decision": result})
		},
	})
	jevCmd.AddCommand(&cobra.Command{
		Use: "classify <captured-id>", Short: "Classify one captured request using scrubbed metadata only", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := findRequestByID(args[0])
			if request == nil {
				return reportJevError(cmd, fmt.Errorf("captured request was not found"))
			}
			config, err := jev.LoadConfig()
			if err != nil {
				return reportJevError(cmd, err)
			}
			input := jev.TrafficInput{Method: request.Method, URL: request.URL, ResourceType: request.ResourceType}
			if request.Response != nil {
				input.Status = request.Response.Status
				input.ContentType = store.HeaderFirst(request.Response.Headers, "content-type")
			}
			result, err := jev.NewClient(config).Classify(cmd.Context(), input)
			if err != nil {
				return reportJevError(cmd, err)
			}
			return emitJev(cmd, "jev classify", result)
		},
	})
}

func emitJev(cmd *cobra.Command, command string, data any) error {
	if useEnvelope() {
		data = output.WrapData(command, "jev", data)
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

func reportJevError(cmd *cobra.Command, err error) error {
	fmt.Fprintln(cmd.ErrOrStderr(), err)
	return err
}
