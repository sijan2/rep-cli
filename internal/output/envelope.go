package output

// AgentEnvelope wraps JSON responses with metadata the agent needs to make
// good decisions: where the data came from, which filters were active, what
// was truncated, and what to try next. Producers populate the fields they
// have; consumers that don't care can ignore them.
type AgentEnvelope struct {
	// Source identifies the backing dataset: "live.json" or "saved/<id>".
	Source string `json:"source,omitempty"`
	// Command is the rep subcommand that produced this payload.
	Command string `json:"command,omitempty"`
	// Filters captures active filter state; freeform to accommodate each
	// command's specific filter surface. Convention: lowercase-keyed and
	// stable across versions.
	Filters map[string]interface{} `json:"filters,omitempty"`
	// Truncation, when non-nil, describes why the payload is not the whole
	// dataset (limit hit, byte cap, spill-to-disk).
	Truncation *TruncationInfo `json:"truncation,omitempty"`
	// Data is the raw result payload; shape is command-specific.
	Data interface{} `json:"data"`
	// Suggest is a list of concrete next commands for the agent to try
	// (e.g. "rep body h_abc --head 8192", "rep list --primary=false").
	Suggest []string `json:"suggest,omitempty"`
}

// WrapData builds the minimum envelope — caller fills in filters/truncation
// afterwards if relevant. Keeping this cheap so every command can adopt it.
func WrapData(command, source string, data interface{}) AgentEnvelope {
	return AgentEnvelope{
		Source:  source,
		Command: command,
		Data:    data,
	}
}
