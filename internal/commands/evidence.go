package commands

import (
	"fmt"

	"github.com/repplus/rep-cli/internal/evidence"
	"github.com/repplus/rep-cli/internal/scope"
)

type evidenceOpener func() (*evidence.Store, map[string]string, error)

func openScopedEvidence() (*evidence.Store, map[string]string, error) {
	selected, err := scope.Current()
	if err != nil {
		return nil, nil, err
	}
	if !selected.Scoped || selected.TaskSource == "default" || selected.Task == "" {
		return nil, nil, fmt.Errorf("evidence requires an explicit workspace and task; use a post-verb --task PROJECT/TASK")
	}
	store, err := evidence.Open(selected.DataDir)
	return store, map[string]string{"workspace": selected.Workspace, "task": selected.Task}, err
}
