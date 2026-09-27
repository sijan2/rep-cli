package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/repplus/rep-cli/internal/evidence"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

type evidenceOpener func() (*evidence.Store, map[string]string, error)

func openScopedEvidence() (*evidence.Store, map[string]string, error) {
	selected, err := scope.Current()
	if err != nil {
		return nil, nil, err
	}
	if !selected.Scoped || selected.TaskSource == "default" || selected.Task == "" {
		return nil, nil, fmt.Errorf("evidence requires an explicit workspace and task; use --workspace PROJECT --task TASK")
	}
	store, err := evidence.Open(selected.DataDir)
	return store, map[string]string{"workspace": selected.Workspace, "task": selected.Task}, err
}

func newEvidenceCommand(open evidenceOpener) *cobra.Command {
	command := &cobra.Command{Use: "evidence", Short: "Record task-scoped runs, operations, and native diagnostic artifacts", Long: "Keep explicit run journals and immutable imported bytes. Commands return bounded JSON. Read rep describe evidence for limits and provenance semantics."}
	var intent, stop, beginManifest string
	begin := &cobra.Command{Use: "begin", Short: "Create a run with intent and an optional stopping condition", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		var spec struct {
			Intent   string            `json:"intent"`
			Stop     string            `json:"stop"`
			Identity map[string]string `json:"identity"`
		}
		if beginManifest != "" {
			if err := readEvidenceManifest(beginManifest, &spec); err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("intent") {
			spec.Intent = intent
		}
		if cmd.Flags().Changed("stop") {
			spec.Stop = stop
		}
		if strings.TrimSpace(spec.Intent) == "" {
			return fmt.Errorf("--intent or manifest intent is required")
		}
		s, identity, err := open()
		if err != nil {
			return err
		}
		if spec.Identity == nil {
			spec.Identity = map[string]string{}
		}
		for k, v := range identity {
			if previous, ok := spec.Identity[k]; ok && previous != v {
				return fmt.Errorf("manifest %s conflicts with selected scope", k)
			}
			spec.Identity[k] = v
		}
		result, err := s.BeginRun(spec.Intent, spec.Stop, spec.Identity)
		return evidenceResult(cmd, result, err)
	}}
	begin.Flags().StringVar(&intent, "intent", "", "Purpose of this run")
	begin.Flags().StringVar(&stop, "stop", "", "Intended stopping condition")
	begin.Flags().StringVar(&beginManifest, "manifest", "", "JSON file with intent, stop, and identity metadata")
	command.AddCommand(begin)

	var runAfter string
	var runLimit int
	list := &cobra.Command{Use: "list", Short: "List run manifests in creation order", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.ListRunPage(runAfter, runLimit)
		return evidenceResult(cmd, result, err)
	}}
	evidencePageFlags(list, &runAfter, &runLimit)
	command.AddCommand(list)
	command.AddCommand(&cobra.Command{Use: "show RUN", Short: "Read one run manifest", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.LoadRun(args[0])
		return evidenceResult(cmd, result, err)
	}})

	var opAfter string
	var opLimit int
	ops := &cobra.Command{Use: "operations RUN", Short: "Page through the append-only operation journal", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.ListOperations(args[0], opAfter, opLimit)
		return evidenceResult(cmd, result, err)
	}}
	evidencePageFlags(ops, &opAfter, &opLimit)
	command.AddCommand(ops)
	command.AddCommand(&cobra.Command{Use: "operation RUN OPERATION", Short: "Read one immutable operation record", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.LoadOperation(args[0], args[1])
		return evidenceResult(cmd, result, err)
	}})
	var opManifest string
	record := &cobra.Command{Use: "record RUN --manifest FILE", Short: "Record an observed operation, including a failed or unknown trial", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var op evidence.Operation
		if err := readEvidenceManifest(opManifest, &op); err != nil {
			return err
		}
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.RecordOperation(args[0], op)
		return evidenceResult(cmd, result, err)
	}}
	record.Flags().StringVar(&opManifest, "manifest", "", "Operation JSON file (kind and status required)")
	_ = record.MarkFlagRequired("manifest")
	command.AddCommand(record)

	var importManifest string
	importCmd := &cobra.Command{Use: "import RUN FILE", Short: "Import native diagnostic bytes without decoding or executing them", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		var spec evidence.ImportSpec
		if importManifest != "" {
			if err := readEvidenceManifest(importManifest, &spec); err != nil {
				return err
			}
		}
		if spec.Path != "" && spec.Path != args[1] {
			return fmt.Errorf("manifest path conflicts with artifact argument")
		}
		spec.Path = args[1]
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.ImportArtifact(args[0], spec)
		return evidenceResult(cmd, result, err)
	}}
	importCmd.Flags().StringVar(&importManifest, "manifest", "", "Optional JSON with collector, build, device, trial, coverage, and metadata")
	command.AddCommand(importCmd)
	var artAfter string
	var artLimit int
	artifacts := &cobra.Command{Use: "artifacts RUN", Short: "Page through imported artifact manifests", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.ListArtifacts(args[0], artAfter, artLimit)
		return evidenceResult(cmd, result, err)
	}}
	evidencePageFlags(artifacts, &artAfter, &artLimit)
	command.AddCommand(artifacts)
	command.AddCommand(&cobra.Command{Use: "artifact RUN ARTIFACT", Short: "Inspect source metadata, byte identity, and declared coverage", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.LoadArtifact(args[0], args[1])
		return evidenceResult(cmd, result, err)
	}})
	var offset int64
	var length int
	read := &cobra.Command{Use: "read RUN ARTIFACT", Short: "Read a bounded byte range as base64 JSON", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		result, err := s.ReadArtifact(args[0], args[1], offset, length)
		return evidenceResult(cmd, result, err)
	}}
	read.Flags().Int64Var(&offset, "offset", 0, "Byte offset")
	read.Flags().IntVar(&length, "length", 4096, "Maximum bytes to read (up to 65536)")
	command.AddCommand(read)
	command.AddCommand(&cobra.Command{Use: "verify RUN ARTIFACT", Short: "Stream stored bytes and check their recorded SHA-256", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		if err := s.VerifyArtifact(args[0], args[1]); err != nil {
			return err
		}
		return evidenceResult(cmd, map[string]any{"artifact_id": args[1], "byte_integrity": "verified", "coverage": "not_established_by_hash"}, nil)
	}})
	var compareLimit int
	var leftAfter, rightAfter string
	compare := &cobra.Command{Use: "compare LEFT_RUN RIGHT_RUN", Short: "Compare declared run metadata and artifact byte identities", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := open()
		if err != nil {
			return err
		}
		left, err := s.LoadRun(args[0])
		if err != nil {
			return err
		}
		right, err := s.LoadRun(args[1])
		if err != nil {
			return err
		}
		lp, err := s.ListArtifacts(left.ID, leftAfter, compareLimit)
		if err != nil {
			return err
		}
		rp, err := s.ListArtifacts(right.ID, rightAfter, compareLimit)
		if err != nil {
			return err
		}
		shared := sharedArtifactContent(lp.Artifacts, rp.Artifacts)
		return evidenceResult(cmd, map[string]any{
			"left_run": left, "right_run": right, "same_identity": reflect.DeepEqual(left.Identity, right.Identity),
			"same_intent": left.Intent == right.Intent, "same_stop": left.Stop == right.Stop,
			"left_artifacts": lp, "right_artifacts": rp, "shared_content": shared,
			"covers_all_artifact_manifests": leftAfter == "" && rightAfter == "" && !lp.HasMore && !rp.HasMore,
			"interpretation":                "Byte equality does not establish identical behavior or complete collection. Metadata is declared by the collector or importer.",
		}, nil)
	}}
	compare.Flags().IntVar(&compareLimit, "limit", evidence.DefaultLimit, "Maximum artifact manifests per run (up to 100)")
	compare.Flags().StringVar(&leftAfter, "left-after", "", "Left artifact cursor")
	compare.Flags().StringVar(&rightAfter, "right-after", "", "Right artifact cursor")
	command.AddCommand(compare)
	return command
}

func evidencePageFlags(cmd *cobra.Command, after *string, limit *int) {
	cmd.Flags().StringVar(after, "after", "", "Resume after this record ID")
	cmd.Flags().IntVar(limit, "limit", evidence.DefaultLimit, "Maximum records (up to 100; byte budget also applies)")
}

func readEvidenceManifest(path string, value any) error {
	if path == "" {
		return fmt.Errorf("manifest file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > evidence.MaxRecordBytes {
		return fmt.Errorf("manifest must be a regular JSON file no larger than %d bytes", evidence.MaxRecordBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > evidence.MaxRecordBytes {
		return fmt.Errorf("manifest must be a regular JSON file no larger than %d bytes", evidence.MaxRecordBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, evidence.MaxRecordBytes+1))
	if err != nil {
		return err
	}
	if len(data) > evidence.MaxRecordBytes {
		return fmt.Errorf("manifest is too large")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		return fmt.Errorf("manifest must contain one JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid evidence manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("manifest must contain exactly one JSON object")
	}
	return nil
}

func evidenceResult(cmd *cobra.Command, result any, err error) error {
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
}

type artifactContentMatch struct {
	SHA256 string   `json:"sha256"`
	Left   []string `json:"left_ids"`
	Right  []string `json:"right_ids"`
}

func sharedArtifactContent(left, right []evidence.Artifact) []artifactContentMatch {
	leftByHash := map[string][]string{}
	rightByHash := map[string][]string{}
	for _, a := range left {
		leftByHash[a.SHA256] = append(leftByHash[a.SHA256], a.ID)
	}
	for _, a := range right {
		rightByHash[a.SHA256] = append(rightByHash[a.SHA256], a.ID)
	}
	keys := []string{}
	for hash := range leftByHash {
		if len(rightByHash[hash]) > 0 {
			keys = append(keys, hash)
		}
	}
	sort.Strings(keys)
	result := []artifactContentMatch{}
	for _, hash := range keys {
		result = append(result, artifactContentMatch{SHA256: hash, Left: leftByHash[hash], Right: rightByHash[hash]})
	}
	return result
}

func init() {
	rootCmd.AddCommand(newEvidenceCommand(openScopedEvidence))
}
