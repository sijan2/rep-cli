package cmd

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	noteRefs   []string
	noteTags   []string
	noteExport bool
	noteClear  bool
	noteDelete string
)

var noteCmd = &cobra.Command{
	Use:   "note [content]",
	Short: "Manage persistent findings",
	Long: `Add, list, and manage persistent notes/findings.

Examples:
  rep note "IDOR in /api/users/:id"
  rep note "Auth bypass" --ref h6f2d
  rep notes
  rep notes --export`,
	Run: func(cmd *cobra.Command, args []string) {
		if noteClear {
			clearNotes()
			return
		}
		if noteDelete != "" {
			deleteNote(noteDelete)
			return
		}
		if len(args) == 0 {
			listNotes()
			return
		}
		addNote(strings.Join(args, " "))
	},
}

func addNote(content string) {
	ns, err := store.LoadNotes()
	if err != nil {
		pterm.Error.Println("Failed to load notes:", err)
		return
	}

	note := ns.AddNote(content, noteRefs, noteTags)

	if err := store.AppendNoteLog(&note); err != nil {
		pterm.Error.Println("Failed to save notes:", err)
		return
	}

	if jsonOutput {
		out := map[string]interface{}{
			"id":      note.ID,
			"hash_id": note.HashID,
			"content": content,
			"refs":    noteRefs,
			"tags":    noteTags,
		}
		data, _ := sonic.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
	} else {
		pterm.Success.Printf("Added note #%d (%s)\n", note.ID, note.HashID)
	}
}

func listNotes() {
	ns, err := store.LoadNotes()
	if err != nil {
		pterm.Error.Println("Failed to load notes:", err)
		return
	}

	if len(ns.Notes) == 0 {
		if jsonOutput {
			fmt.Println("[]")
		} else {
			pterm.Info.Println("No notes yet")
		}
		return
	}

	if jsonOutput {
		if noteExport {
			// Export format for reports
			var export []map[string]interface{}
			for _, n := range ns.Notes {
				export = append(export, map[string]interface{}{
					"id":        n.ID,
					"hash_id":   n.HashID,
					"content":   n.Content,
					"refs":      n.Refs,
					"tags":      n.Tags,
					"timestamp": n.Timestamp.Format("2006-01-02 15:04"),
				})
			}
			data, _ := sonic.MarshalIndent(export, "", "  ")
			fmt.Println(string(data))
		} else {
			data, _ := sonic.MarshalIndent(ns.Notes, "", "  ")
			fmt.Println(string(data))
		}
		return
	}

	// Human-readable output
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("Session Notes (%d findings)\n", len(ns.Notes))
	fmt.Println(strings.Repeat("─", 50))

	for _, n := range ns.Notes {
		fmt.Printf("#%d [%s] %s\n", n.ID, n.Timestamp.Format("2006-01-02 15:04"), n.Content)
		fmt.Printf("   id: %s\n", n.HashID)
		if len(n.Refs) > 0 {
			fmt.Printf("   ref: %s\n", strings.Join(n.Refs, ", "))
		}
		if len(n.Tags) > 0 {
			fmt.Printf("   tags: %s\n", strings.Join(n.Tags, ", "))
		}
	}
	fmt.Println(strings.Repeat("─", 50))
}

func deleteNote(identifier string) {
	ns, err := store.LoadNotes()
	if err != nil {
		pterm.Error.Println("Failed to load notes:", err)
		return
	}

	note := ns.FindNoteByIdentifier(identifier)
	if note == nil {
		pterm.Error.Printf("Note not found: %s\n", identifier)
		return
	}

	if err := store.AppendNoteDelete(note.HashID); err != nil {
		pterm.Error.Println("Failed to save notes:", err)
		return
	}

	if jsonOutput {
		fmt.Printf(`{"deleted": "%s"}`, note.HashID)
		fmt.Println()
	} else {
		pterm.Success.Printf("Deleted note #%d (%s)\n", note.ID, note.HashID)
	}
}

func clearNotes() {
	ns, err := store.LoadNotes()
	if err != nil {
		pterm.Error.Println("Failed to load notes:", err)
		return
	}

	count := len(ns.Notes)
	ns.ClearNotes()

	if err := store.AppendNotesClear(); err != nil {
		pterm.Error.Println("Failed to save notes:", err)
		return
	}

	if jsonOutput {
		fmt.Printf(`{"cleared": %d}`, count)
		fmt.Println()
	} else {
		pterm.Success.Printf("Cleared %d notes\n", count)
	}
}

func init() {
	rootCmd.AddCommand(noteCmd)
	noteCmd.Flags().StringSliceVar(&noteRefs, "ref", nil, "Request IDs to reference")
	noteCmd.Flags().StringSliceVar(&noteTags, "tag", nil, "Tags for the note")
	noteCmd.Flags().BoolVar(&noteExport, "export", false, "Export notes for report")
	noteCmd.Flags().BoolVar(&noteClear, "clear", false, "Clear all notes")
	noteCmd.Flags().StringVar(&noteDelete, "delete", "", "Delete note by ID or hash ID")
}
