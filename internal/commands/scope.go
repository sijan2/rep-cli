package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/headless"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type scopeBrowserProfiles struct {
	Extension              scopeExtensionProfile `json:"extension"`
	TaskHeadless           scopeTaskProfile      `json:"task_headless"`
	ArchiveAccountIdentity string                `json:"archive_account_identity"`
}

type scopeExtensionProfile struct {
	SharedAcrossTasks bool     `json:"shared_across_tasks"`
	Selectors         []string `json:"selectors"`
}

type scopeTaskProfile struct {
	Available         bool   `json:"available"`
	SharedAcrossTasks bool   `json:"shared_across_tasks"`
	Persistent        bool   `json:"persistent"`
	Selector          string `json:"selector"`
	Status            string `json:"status"`
	ProfilePath       string `json:"profile_path,omitempty"`
	Running           *bool  `json:"running,omitempty"`
	Connected         *bool  `json:"connected,omitempty"`
	Headed            *bool  `json:"headed,omitempty"`
	StartCommand      string `json:"start_command,omitempty"`
}

// Inspect only the selected task's lifecycle status. Do not return the embedded
// headless.State: it contains debugging endpoint and bridge connection details.
func inspectScopeBrowserProfiles(ctx context.Context, selected scope.Scope) scopeBrowserProfiles {
	profiles := scopeBrowserProfiles{
		Extension:              scopeExtensionProfile{SharedAcrossTasks: true, Selectors: []string{"dia", "arc", "chrome", "any"}},
		TaskHeadless:           scopeTaskProfile{Persistent: true, Selector: "headless", Status: "requires_explicit_task"},
		ArchiveAccountIdentity: "not_verified",
	}
	if !selected.Scoped || selected.TaskSource == "default" {
		return profiles
	}
	profile := &profiles.TaskHeadless
	profile.Available = true
	profile.ProfilePath = filepath.Join(selected.DataDir, "headless", "profile")
	profile.StartCommand = fmt.Sprintf("rep --workspace %s --task %s browser headless start", selected.Workspace, selected.Task)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := headless.GetStatus(ctx, selected.DataDir)
	if err != nil {
		profile.Status = "unknown"
		return profiles
	}
	profile.Running, profile.Connected, profile.Headed = &status.Running, &status.Connected, &status.Headed
	switch {
	case status.Reason == "not_started":
		profile.Status = "not_started"
	case !status.Running:
		profile.Status = "stopped"
	case !status.Connected:
		profile.Status = "disconnected"
	default:
		profile.Status = "ready"
	}
	return profiles
}

var scopeCmd = &cobra.Command{
	Use: "scope", Short: "Show workspace/task data paths and browser profile isolation",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		selected, err := scope.Current()
		if err != nil {
			return err
		}
		selected.LivePath, err = store.GetLiveFilePath()
		if err != nil {
			return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError(output.ErrCodeInvalidArgument, "scope", err.Error()), getOutputMode() == "json")
		}
		status := "not_captured"
		if _, err := os.Stat(selected.LivePath); err == nil {
			status = "available"
		} else if !os.IsNotExist(err) {
			status = "unreadable"
		}
		result := struct {
			scope.Scope
			CaptureStatus string `json:"capture_status"`
			// Retained for compatibility; this describes the shared extension
			// selectors. Task headless isolation is reported separately below.
			SharedBrowser   bool                 `json:"shared_browser_profile"`
			NeedsSelection  bool                 `json:"needs_selection"`
			BrowserProfiles scopeBrowserProfiles `json:"browser_profiles"`
		}{
			Scope: selected, CaptureStatus: status, SharedBrowser: true,
			NeedsSelection:  (!selected.Scoped && !selected.ExplicitGlobal) || (selected.Scoped && selected.TaskSource == "default"),
			BrowserProfiles: inspectScopeBrowserProfiles(cmd.Context(), selected),
		}
		if getOutputMode() == "json" {
			data, _ := sonic.MarshalIndent(result, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
		} else {
			if selected.Scoped {
				fmt.Fprintf(cmd.OutOrStdout(), "workspace: %s\ntask: %s\n", selected.Workspace, selected.Task)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "workspace: shared legacy data")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "selection: %s\ndata: %s\ncapture: %s\n", selected.Source, selected.DataDir, status)
			if result.NeedsSelection {
				fmt.Fprintln(cmd.OutOrStdout(), "Select --workspace <project> --task <task> (or REP_TASK within a bound project), or use --global for legacy data.")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Extension browsers (arc/chrome/any) share cookies, accounts, and tabs across tasks; use a task-owned tab.")
			profile := result.BrowserProfiles.TaskHeadless
			if profile.Available {
				fmt.Fprintf(cmd.OutOrStdout(), "Task browser: %s; private persistent profile: %s\n", profile.Status, profile.ProfilePath)
				fmt.Fprintf(cmd.OutOrStdout(), "Start/reuse: %s; select it with --browser headless.\n", profile.StartCommand)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Select an explicit workspace and task to use an isolated persistent headless profile.")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Scope does not select a browser or verify the account that produced an archive.")
		}
		return nil
	},
}

func init() { rootCmd.AddCommand(scopeCmd) }
