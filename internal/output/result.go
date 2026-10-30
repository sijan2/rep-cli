package output

import (
	"encoding/json"
	"io"

	"github.com/repplus/rep-cli/internal/scope"
)

// Result adds only invocation-owned metadata. Never derive hints from content.
// Legacy array commands keep their shape until their removal; core uses objects.
func Result(value any) any {
	selected, ok := scope.Configured()
	if !ok {
		return value
	}
	data, err := Marshal(value)
	if err != nil {
		return value
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return value
	}
	object["task"], _ = json.Marshal(selected.Identity())
	object["task_source"], _ = json.Marshal(selected.TaskSource)
	return object
}
func WriteResult(writer io.Writer, value any) error { return WriteJSON(writer, Result(value)) }
