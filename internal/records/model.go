// Package records extracts threads and their continuation frontier from saved
// response bytes. Decoding and merging do not perform I/O.
package records

type Source struct {
	RequestID string `json:"request_id,omitempty"`
	Op        string `json:"op,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Offset    int    `json:"offset"`
}

type Record struct {
	R         string           `json:"r"`
	ID        string           `json:"id"`
	Parent    string           `json:"parent"`
	Depth     int              `json:"depth"`
	WireDepth *int             `json:"wire_depth,omitempty"`
	Kind      string           `json:"kind"`
	Author    string           `json:"author"`
	Time      string           `json:"time"`
	Text      string           `json:"text"`
	URL       string           `json:"url,omitempty"`
	Metrics   map[string]int64 `json:"metrics"`
	Flags     []string         `json:"flags,omitempty"`
	DOMKey    string           `json:"dom_key,omitempty"`
	Src       Source           `json:"src"`
}

type ContinuationKey struct {
	Path   string `json:"path"`
	Cursor string `json:"cursor,omitempty"`
}

type Yield struct {
	Records     int `json:"records"`
	NestedHints int `json:"nested_hints"`
}

type Continuation struct {
	Kind      string          `json:"kind"`
	Parent    string          `json:"parent"`
	CountHint int             `json:"count_hint"`
	Label     string          `json:"label,omitempty"`
	Key       ContinuationKey `json:"key"`
	Src       string          `json:"src"`
	State     string          `json:"state"`
	Reason    string          `json:"reason,omitempty"`
	Yield     Yield           `json:"yield"`
}

type Page struct {
	Site          string         `json:"site"`
	Op            string         `json:"op"`
	Thread        *Record        `json:"thread,omitempty"`
	Records       []Record       `json:"records"`
	Continuations []Continuation `json:"continuations"`
	Expected      int            `json:"expected"`
	Dropped       map[string]int `json:"dropped"`
	Terminal      bool           `json:"terminal"`
	Class         string         `json:"class"`
}

type Coverage struct {
	Records           int                `json:"records"`
	Expected          int                `json:"expected"`
	Ratio             float64            `json:"ratio"`
	FrontierExhausted bool               `json:"frontier_exhausted"`
	Status            string             `json:"status"`
	Code              string             `json:"code"`
	GapReason         string             `json:"gap_reason,omitempty"`
	Pending           int                `json:"pending"`
	ResolvedShort     int                `json:"resolved_short"`
	Dropped           map[string]int     `json:"dropped"`
	NullRates         map[string]float64 `json:"null_rates"`
	Breaches          []string           `json:"breaches"`
}

func newPage(site, op string) Page {
	return Page{Site: site, Op: op, Expected: -1, Class: "ok", Records: []Record{}, Continuations: []Continuation{}, Dropped: map[string]int{}}
}

func hasFlag(r Record, flag string) bool {
	for _, value := range r.Flags {
		if value == flag {
			return true
		}
	}
	return false
}

func addFlag(r *Record, flag string) {
	if !hasFlag(*r, flag) {
		r.Flags = append(r.Flags, flag)
	}
}
