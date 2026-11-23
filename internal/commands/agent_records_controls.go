package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/ledger"
	"github.com/repplus/rep-cli/internal/records"
	"github.com/repplus/rep-cli/internal/scene"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/sitepolicy"
	"github.com/spf13/cobra"
)

// The geometry budget bounds what one perception round trip moves across
// native messaging; the extension drops far-away text first, never elements.
const recordsGeometryBytes = 512 << 10

// sceneIndex is what `rep do n_...` validates a handle against: the control's
// role, masked name and owning record as they were when the agent saw them.
type sceneIndex struct {
	Tab      string                   `json:"tab"`
	URL      string                   `json:"url"`
	Frame6   string                   `json:"frame6"`
	Created  time.Time                `json:"created"`
	Controls map[string]scene.Control `json:"controls"`
}

func sceneIndexPath(dataDir, tab string) string {
	return filepath.Join(dataDir, "scene", tab+".json")
}

func newCoreRecordsControlsCommand() *cobra.Command {
	command := &cobra.Command{Use: "controls TAB", Short: "Show records with the controls each one owns", Args: cobra.ExactArgs(1), Long: `One line per visible record: handle, depth, author, time, metric, text, then
the controls inside it as n_ handles. Act on one with rep do n_HANDLE TAB.`}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreRecordsControls(cmd, args[0]); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}

func coreRecordsControls(command *cobra.Command, target string) error {
	if !agentstate.TabPattern.MatchString(target) {
		return coreArgument("controls takes an owned tab handle")
	}
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	owner := selected.Identity()
	addressed, err := store.Tab(target)
	if err != nil {
		return err
	}
	if addressed.Owner != owner {
		return agentstate.Failure("owned_by_other_task", "controls reads only this task's tabs", coreNext("tabs"))
	}
	if !addressed.CreatedByRep {
		return agentstate.Failure("user_tab", "controls refuses a tab that rep did not create", coreNext("tabs"))
	}
	started := time.Now()
	ctx, cancel := coreTimeout(command, 20*time.Second)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	tab, lease, err := store.Reserve(browser, owner, selected.SessionKey, target, selected.TaskSource == "flag", false, 25*time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	var raw json.RawMessage
	if err := client.Call(ctx, "browser.geometry", map[string]any{"tab_id": tab.ChromeID, "owner": owner, "max_bytes": recordsGeometryBytes}, &raw); err != nil {
		return err
	}
	snapshot, err := scene.Decode(raw)
	if err != nil {
		return agentstate.Failure("geometry_invalid", "the browser returned an unusable geometry snapshot", coreNext("page", tab.Handle))
	}
	input := scene.Input{Snapshot: snapshot, Keys: scene.KeysForURL(snapshot.URL)}
	// Network records from the tab's ledger give reading order and the
	// records the DOM does not show. Their absence only loses that join.
	networkRecords, networkStats := recordsForScene(ctx, client, tab, owner, snapshot.URL)
	input.Network = networkRecords
	graph, err := scene.Build(input)
	if err != nil {
		return agentstate.Failure("scene_failed", "the geometry snapshot could not be segmented", coreNext("page", tab.Handle))
	}
	projection := graph.Project(scene.View{Mode: "viewport", MaxBytes: scene.DefaultViewBytes})
	index := sceneIndex{Tab: tab.Handle, URL: snapshot.URL, Frame6: graph.Frame6, Created: time.Now().UTC(), Controls: map[string]scene.Control{}}
	owned := 0
	for _, record := range graph.Records {
		for _, control := range record.Controls {
			index.Controls[control.H] = control
			owned++
		}
	}
	for _, control := range graph.Orphans {
		index.Controls[control.H] = control
	}
	if err := writeSceneIndex(selected.DataDir, index); err != nil {
		return err
	}
	_ = store.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.URL = snapshot.URL; return nil })
	netOnly := 0
	for _, record := range graph.Records {
		if record.NetOnly {
			netOnly++
		}
	}
	result := coreTabResult(tab)
	result.Browser, result.Profile = browser.Name, browser.Profile
	result.Status = "ready"
	result.Complete = boolPointer(!projection.Truncated)
	result.Meta = map[string]any{"frame6": graph.Frame6, "records": len(graph.Records), "net_only": netOnly, "controls": owned, "orphans": len(graph.Orphans), "stats": graph.Stats, "network": networkStats, "truncated": projection.Truncated, "model_requests": 0, "timing_ms": map[string]int64{"total": time.Since(started).Milliseconds()}, "scene_index": sceneIndexPath(selected.DataDir, tab.Handle)}
	result.Untrusted = map[string]any{"url": snapshot.URL, "lines": projection.Lines, "records": projection.Records}
	return emitCore(command, result)
}

// recordsForScene decodes the ledger of a tab a records read left open. A tab
// without one yields no join; nothing is fetched to make one.
func recordsForScene(ctx context.Context, client ledger.Caller, tab *agentstate.Tab, owner, pageURL string) ([]records.Record, map[string]any) {
	stats := map[string]any{"joined": false}
	if tab.Ledger == "" {
		return nil, stats
	}
	policy, _, ok := sitepolicy.Match(pageURL)
	if !ok {
		return nil, stats
	}
	book := &ledger.Client{Caller: client, Tab: tab.ChromeID, Owner: owner, ID: tab.Ledger}
	batch, err := book.Read(ctx, 0, ledger.Match{AdmittedOnly: true, Phase: "end"}, 0)
	if err != nil || batch.State == "closed" {
		stats["error"] = "ledger_unavailable"
		return nil, stats
	}
	latest := map[uint64]ledger.Entry{}
	var order []uint64
	for _, entry := range batch.Entries {
		if _, seen := latest[entry.Seq]; !seen {
			order = append(order, entry.Seq)
		}
		latest[entry.Seq] = entry
	}
	var inputs []recordsInput
	var primary *recordsInput
	for _, seq := range order {
		entry := latest[seq]
		if entry.Response == nil || entry.Body.State != "complete" {
			continue
		}
		op, admitted := policy.Admit(entry.Method, entry.URL, entry.Type)
		if !admitted {
			continue
		}
		body, sha, err := book.Body(ctx, seq)
		if err != nil {
			continue
		}
		input := recordsLedgerInput(entry, body, sha)
		if op == policy.Primary.Op && primary == nil {
			primary = &input
			continue
		}
		inputs = append(inputs, input)
	}
	if primary == nil {
		return nil, stats
	}
	thread, err := recordsDecodeAll(append([]recordsInput{*primary}, inputs...), "auto", recordsFocal(pageURL))
	if err != nil {
		stats["error"] = "decode_failed"
		return nil, stats
	}
	ordered := thread.Ordered()
	stats["joined"], stats["records"] = true, len(ordered)
	return ordered, stats
}

func writeSceneIndex(dataDir string, index sceneIndex) error {
	path := sceneIndexPath(dataDir, index.Tab)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	// The index is replaced on every view; a stale one must never validate
	// a handle from an earlier document, so frame6 is checked at act time too.
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func readSceneIndex(dataDir, tab string) (sceneIndex, error) {
	var index sceneIndex
	data, err := os.ReadFile(sceneIndexPath(dataDir, tab))
	if err != nil {
		return index, err
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return index, err
	}
	return index, nil
}

// leasedCDP lets the act package's raw browser.cdp calls ride the tab's
// execute lease: the extension routes any owner-bearing CDP call through the
// lease path, which refuses calls that carry no lease.
type leasedCDP struct{ session *browserfast.Session }

func (c leasedCDP) Call(ctx context.Context, method string, params any, out any) error {
	if method != "browser.cdp" {
		return c.session.Call(ctx, method, params, out)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"command_params"`
	}
	if err := json.Unmarshal(encoded, &request); err != nil {
		return err
	}
	var result json.RawMessage
	if err := c.session.Inspect(ctx, "", request.Method, request.Params, &result); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	wrapped, err := json.Marshal(map[string]json.RawMessage{"result": result})
	if err != nil {
		return err
	}
	return json.Unmarshal(wrapped, out)
}
