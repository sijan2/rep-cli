// Package jevrpc defines host-local decisions. Requests containing credentials
// travel only over the private Unix socket and must never reach the extension.
package jevrpc

import "github.com/repplus/rep-cli/internal/jevdom"

const Version = 1
const MaxBatch = 32

type Credentials struct {
	APIKey string `json:"api_key"`
}

type SelectRequest struct {
	Version     int              `json:"version"`
	Credentials *Credentials     `json:"credentials,omitempty"`
	Options     []jevdom.Options `json:"options"`
}

type BatchResult struct {
	Results []jevdom.Result `json:"results"`
}

// StepRequest asks the host for one operation and target decision.
type StepRequest struct {
	Version     int                `json:"version"`
	Credentials *Credentials       `json:"credentials,omitempty"`
	Options     jevdom.StepOptions `json:"options"`
}

type Capabilities struct {
	Version             int  `json:"version"`
	PersistentSelection bool `json:"persistent_selection"`
	BatchSelection      bool `json:"batch_selection"`
	MaxBatch            int  `json:"max_batch"`
	StepDecision        bool `json:"step_decision,omitempty"`
}
