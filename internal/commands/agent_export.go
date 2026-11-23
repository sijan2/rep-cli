package commands

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

// Export never contacts a browser or server. It preserves retained request
// credentials for tools that cannot use an in-browser fetch's header rules.
func newCoreExportCommand() *cobra.Command {
	format := "json"
	command := &cobra.Command{Use: "export h_ID PATH", Short: "Export original captured headers and bodies, or a runnable curl script", Args: cobra.ExactArgs(2)}
	command.Long = `Writes a new file with original URL, Cookie/Authorization/CSRF headers and
request/response bodies. JSON retains the native captured record and decoded
body bytes as base64, with source archive and completeness. Partial evidence
stays partial. curl writes a shell script without executing it; it requires a
complete HTTP request body, retains credentials, and recomputes framing headers.
Scripts with bodies use python3 and a temporary file for exact bytes. No request is
sent by export, and no login state is fetched again.`
	command.Example = "  rep export h_ID captured-request.json\n  rep export h_ID request.sh --format curl\n  sh request.sh"
	command.Flags().StringVar(&format, "format", "json", "Export format: json or curl")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreExport(cmd, args[0], args[1], format); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}

type exportedBody struct {
	ContentType     string            `json:"content_type"`
	Encoding        string            `json:"encoding"`
	Data            string            `json:"data"`
	Capture         store.BodyCapture `json:"capture"`
	ValidationError string            `json:"validation_error,omitempty"`
}

func exportBody(request *store.Request, requestSide bool) (exportedBody, error) {
	data, contentType, evidence, err := bodyEvidence(request, requestSide)
	result := exportedBody{ContentType: contentType, Encoding: "base64", Data: base64.StdEncoding.EncodeToString(data), Capture: evidence}
	if err != nil {
		result.Capture.State = "invalid"
		result.ValidationError = err.Error()
	}
	return result, err
}

func completeExportBody(body exportedBody) bool {
	return body.Capture.State == "complete" || body.Capture.State == "not_applicable"
}

func exportCurl(request *store.Request, body exportedBody) ([]byte, error) {
	if request.RecordKind != "" && request.RecordKind != "http" {
		return nil, coreArgument("curl export requires an HTTP request; use --format json for stream evidence")
	}
	if !completeExportBody(body) {
		return nil, agentstate.Failure("request_body_incomplete", "curl export requires a complete captured request body; JSON preserves partial evidence", coreNext("export", "--help"))
	}
	if _, err := coreRequestURL(request.URL); err != nil {
		return nil, err
	}
	if strings.ContainsAny(request.URL, "\r\n\x00") {
		return nil, coreArgument("captured URL contains control characters")
	}
	method := request.Method
	if !coreHTTPToken.MatchString(method) {
		return nil, coreArgument("captured HTTP method is invalid")
	}
	// --globoff preserves URLs containing brackets/braces. --path-as-is avoids
	// normalizing observed paths. Redirects remain explicit user choices.
	args := []string{"curl", "--disable", "--globoff", "--path-as-is", "--request", method, "--url", request.URL}
	if method == "HEAD" {
		args = append(args, "--head")
	}
	names := make([]string, 0, len(request.Headers))
	for name := range request.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch strings.ToLower(name) {
		case "content-length", "transfer-encoding", "connection", "proxy-connection", ":method", ":scheme", ":path", ":authority":
			continue
		}
		if !coreHTTPToken.MatchString(name) {
			return nil, coreArgument("captured header name is invalid")
		}
		for _, value := range request.Headers[name] {
			if strings.ContainsAny(value, "\r\n\x00") {
				return nil, coreArgument("captured header value contains control characters")
			}
			header := name + ": " + value
			if value == "" {
				header = name + ";"
			} // curl's syntax for a real empty header
			args = append(args, "--header", header)
		}
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\n# Exported captured request. Running this file sends the request once.\nset -eu\n")
	if body.Capture.State == "complete" && (body.Data != "" || !coreSafeMethod(method)) {
		// Decode before dispatch so missing Python or a decoding failure cannot
		// turn the export into an empty-body write. A heredoc avoids ARG_MAX for
		// large payloads. The file is removed even when curl exits with an error.
		script.WriteString("body_file=$(mktemp \"${TMPDIR:-/tmp}/rep-export.XXXXXXXX\")\ntrap 'rm -f \"$body_file\"' EXIT\ntrap 'exit 130' INT\ntrap 'exit 143' TERM\npython3 - \"$body_file\" <<'REP_EXPORT_BODY'\n")
		fmt.Fprintf(&script, "import base64,pathlib,sys\npathlib.Path(sys.argv[1]).write_bytes(base64.b64decode(%q, validate=True))\nREP_EXPORT_BODY\n", body.Data)
		script.WriteString(quoteCommand(args...) + " --data-binary \"@$body_file\"\n")
	} else {
		script.WriteString("exec " + quoteCommand(args...) + "\n")
	}
	return []byte(script.String()), nil
}

func coreExport(command *cobra.Command, handle, path, format string) error {
	if strings.TrimSpace(path) == "" {
		return coreArgument("export requires an explicit new file path")
	}
	if format != "json" && format != "curl" {
		return coreArgument("export --format must be json or curl")
	}
	capture, request, err := coreDataRequest(handle)
	if err != nil {
		return err
	}
	// JSON always keeps the original record, even if its body evidence is
	// corrupt. It records that validation failure rather than inventing bytes.
	requestBody, _ := exportBody(request, true)
	responseBody, _ := exportBody(request, false)
	complete := completeExportBody(requestBody) && completeExportBody(responseBody)
	var data []byte
	if format == "curl" {
		data, err = exportCurl(request, requestBody)
		complete = completeExportBody(requestBody)
	} else {
		data, err = json.MarshalIndent(map[string]any{"schema_version": 1, "source_archive": capture.ID, "complete": complete, "record": request, "request_body": requestBody, "response_body": responseBody}, "", "  ")
		data = append(data, '\n')
	}
	if err != nil {
		return err
	}
	artifact, err := coreArtifact(path, "", data)
	if err != nil {
		return err
	}
	return emitCore(command, coreResult{Browser: capture.Browser.Name, Profile: capture.Browser.Profile, Status: "exported", Complete: boolPointer(complete), Meta: map[string]any{"format": format, "request_id": request.ID, "source_archive": capture.ID, "artifact": artifact, "source_complete": complete}})
}

func init() { rootCmd.AddCommand(newCoreExportCommand()) }
