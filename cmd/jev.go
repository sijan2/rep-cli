package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/repplus/rep-cli/internal/bridge"
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

// reportJevError writes a Jev, selection, or browser failure once, with a
// stable code and next steps. JSON callers receive the error object on stdout,
// like other commands; text callers receive it on stderr.
func reportJevError(cmd *cobra.Command, err error) error {
	ae := jevAgentError(cmd.CommandPath(), err)
	if getOutputMode() == "json" {
		return output.EmitAgentError(cmd.OutOrStdout(), ae, true)
	}
	return output.EmitAgentError(cmd.ErrOrStderr(), ae, false)
}

// jevAgentError keeps the classified cause: a Jev failure code from this
// process, the code a host relayed for its own Jev failure, or a browser code.
func jevAgentError(command string, err error) output.AgentError {
	var existing output.AgentError
	if errors.As(err, &existing) {
		if existing.Command == "" {
			existing.Command = command
		}
		return existing
	}
	code, message := jev.CodeOf(err), err.Error()
	var rpc *bridge.RPCError
	if code == "" && errors.As(err, &rpc) && rpc.Code != "" {
		code, message = rpc.Code, rpc.Message
	}
	if code == "" {
		code = output.ErrCodeCommandFailed
	}
	return output.NewAgentError(code, command, message, jevSuggestions(code)...)
}

func jevSuggestions(code string) []string {
	switch code {
	case jev.CodeNotConfigured:
		return []string{"rep jev config --env-file /absolute/path/to/.env", "rep jev status -j"}
	case jev.CodeUnauthorized:
		return []string{"rep jev status -j", "rep jev doctor -j"}
	case jev.CodeRateLimited, jev.CodeOverloaded, jev.CodeServerError, jev.CodeTimeout, jev.CodeConnection, jev.CodeUnclassified:
		return []string{"retry after a short wait; the lookup did not act on the page", "rep jev doctor -j"}
	case jev.CodeContextExceeded, jev.CodeInvalidRequest:
		return []string{"narrow the lookup with --frame and --root-node, or lower --limit", "rep browser observe --tab ID --kind KIND  # inspect candidates without a model call"}
	case jev.CodeInvalidResponse:
		return []string{"retry; if it repeats, pin JEV_MODEL (for example jev-1.13.0) and keep this message", "rep jev doctor -j"}
	case output.ErrCodeBrowserUnavailable:
		return []string{"rep browser status", "rep browser tabs"}
	}
	return nil
}
