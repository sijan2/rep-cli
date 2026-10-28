package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
)

// runDifferential implements the two oracle upgrades together, because they
// answer the same question: did MY change cause this difference, or does the
// endpoint just do that?
//
//	--baseline N  learn the endpoint's own variation, then judge only stable fields
//	--as a,b,anon re-execute as other principals and compare their access
type differentialPrincipal struct {
	Name          string             `json:"name"`
	Status        int                `json:"status"`
	Bytes         int                `json:"bytes"`
	Comparison    comparison         `json:"comparison"`
	Error         *output.AgentError `json:"error,omitempty"`
	CredentialRef string             `json:"credential_fingerprint,omitempty"`
}

func runDifferential(origReq *store.Request, muts []mutation, jsonMode bool) (returnErr error) {
	// A differential replay is the step that produces findings, so it must be
	// citable: `rep probe record` accepts operation pointers, never prose.
	record, evErr := beginBrowserEvidence("replay.differential", map[string]any{
		"intent": "differential replay", "request_id": origReq.ID,
		"principals": replayAs, "baseline_samples": replayBaseline,
	}, "", -1)
	if evErr != nil {
		return output.EmitAgentError(os.Stdout,
			output.WrapError(evErr, output.ErrCodeInvalidArgument, "replay"), jsonMode)
	}
	defer record.finishOnReturn(&returnErr)
	record.dispatch()

	base, err := applyMutations(origReq, muts)
	if err != nil {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay", err.Error())
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	samples := replayBaseline
	if samples == 0 {
		samples = 2 // --as still needs a reference and a stability estimate
	}
	if samples < 2 {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay",
			"--baseline must be at least 2 to observe variation between identical requests",
			"rep replay <id> --baseline 5")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	// Phase 1: sample the unmutated request as its original principal.
	bodies := make([][]byte, 0, samples)
	statuses := make([]int, 0, samples)
	complete := true
	for i := 0; i < samples; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
		res, err := executeReplay(ctx, base)
		cancel()
		if err != nil {
			ae := output.WrapError(err, output.ErrCodeInternal, "replay",
				"check target reachability / TLS / timeout")
			return output.EmitAgentError(os.Stdout, ae, jsonMode)
		}
		if res.BodyCapture.State != "complete" || res.BodyTruncated || res.BodyError != "" {
			complete = false
		}
		bodies = append(bodies, res.Body)
		statuses = append(statuses, res.Status)
	}
	mask := buildVolatilityMask(bodies, statuses)
	reference, refStatus := bodies[0], statuses[0]

	data := map[string]interface{}{
		"request_id":       origReq.ID,
		"url":              base.URL,
		"method":           base.Method,
		"baseline_samples": samples,
		"volatility":       mask,
		"baseline_status":  refStatus,
	}

	// Phase 2a: no principals requested — report what the endpoint itself does.
	if len(replayAs) == 0 {
		data["interpretation"] = baselineInterpretation(mask)
		if err := finishDifferentialEvidence(record, data, ""); err != nil {
			return output.EmitAgentError(os.Stdout,
				output.WrapError(err, output.ErrCodeInternal, "replay"), jsonMode)
		}
		attachOperationEvidence(data, record)
		return emitDifferential(data, jsonMode, nil)
	}

	// Phase 2b: re-execute as each named principal and compare access.
	all, err := loadIdentities()
	if err != nil {
		return output.EmitAgentError(os.Stdout,
			output.WrapError(err, output.ErrCodeInternal, "replay"), jsonMode)
	}
	results := make([]differentialPrincipal, 0, len(replayAs))
	comparisons := map[string]comparison{}
	statusByName := map[string]int{}

	for _, name := range replayAs {
		entry := differentialPrincipal{Name: name}
		asReq, err := applyIdentity(base, name, all)
		if err != nil {
			ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay", err.Error(),
				"rep identity list", "rep identity capture <request-id> --as "+name)
			entry.Error = &ae
			results = append(results, entry)
			continue
		}
		if rec, ok := all[name]; ok {
			entry.CredentialRef = rec.Fingerprint
		}
		ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
		res, err := executeReplay(ctx, asReq)
		cancel()
		if err != nil {
			ae := output.WrapError(err, output.ErrCodeInternal, "replay")
			entry.Error = &ae
			results = append(results, entry)
			continue
		}
		bodyComplete := complete && res.BodyCapture.State == "complete" && !res.BodyTruncated && res.BodyError == ""
		entry.Status = res.Status
		entry.Bytes = len(res.Body)
		entry.Comparison = compareAgainstBaseline(mask, reference, res.Body, refStatus, res.Status, bodyComplete)
		comparisons[name] = entry.Comparison
		statusByName[name] = res.Status
		results = append(results, entry)
	}

	data["principals"] = results
	owner := "the captured principal"
	verdict := interpretAuthz(owner, comparisons, statusByName)
	data["authz"] = verdict
	if err := finishDifferentialEvidence(record, data, verdict.Verdict); err != nil {
		return output.EmitAgentError(os.Stdout,
			output.WrapError(err, output.ErrCodeInternal, "replay"), jsonMode)
	}
	attachOperationEvidence(data, record)

	return emitDifferential(data, jsonMode, &verdict)
}

func baselineInterpretation(mask volatilityMask) map[string]interface{} {
	out := map[string]interface{}{"usable": mask.Usable}
	switch {
	case !mask.Usable:
		out["summary"] = "could not derive a volatility mask: " + mask.Reason
		out["next"] = "raise --baseline, or treat body comparisons for this endpoint as inconclusive"
	case mask.VolatileCount == 0:
		out["summary"] = fmt.Sprintf("endpoint is deterministic across %d identical requests", mask.Samples)
		out["next"] = "any difference under mutation is attributable to the mutation"
	default:
		out["summary"] = fmt.Sprintf("%d field(s) vary between identical requests and will be ignored; %d are stable",
			mask.VolatileCount, mask.StableCount)
		out["next"] = "mutations are now judged only on the stable fields"
	}
	return out
}

func emitDifferential(data map[string]interface{}, jsonMode bool, verdict *authzVerdict) error {
	env := output.WrapData("replay", "live.json", data)
	env.Filters = map[string]interface{}{
		"as": replayAs, "baseline": replayBaseline, "set": replaySet,
	}
	if verdict != nil && verdict.Verdict == "possible_authz_issue" {
		env.Suggest = []string{
			"rep replay <id> --as " + joinNames(verdict.Reviewing) + " --include-body  # confirm the body holds the owner's resource",
			"rep note --ref <id> --tag authz \"<subject> reached <resource>\"  # record the finding with its subject named",
		}
	}
	if jsonMode {
		buf, err := sonic.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}
	return printDifferentialText(data, verdict)
}

func printDifferentialText(data map[string]interface{}, verdict *authzVerdict) error {
	fmt.Printf("%v %v\n", data["method"], data["url"])
	if mask, ok := data["volatility"].(volatilityMask); ok {
		fmt.Printf("baseline: %d samples, status %v, mask=%s", mask.Samples, data["baseline_status"], mask.Kind)
		if mask.Usable {
			fmt.Printf(" (%d volatile / %d stable fields)\n", mask.VolatileCount, mask.StableCount)
		} else {
			fmt.Printf(" UNUSABLE: %s\n", mask.Reason)
		}
	}
	if interp, ok := data["interpretation"].(map[string]interface{}); ok {
		fmt.Printf("\n%v\n  next: %v\n", interp["summary"], interp["next"])
		return nil
	}
	principals, _ := data["principals"].([]differentialPrincipal)
	if len(principals) > 0 {
		fmt.Println()
		for _, p := range principals {
			if p.Error != nil {
				fmt.Printf("  %-14s ERROR  %s\n", p.Name, p.Error.Message)
				continue
			}
			fmt.Printf("  %-14s %d  %6dB  %-12s %s\n",
				p.Name, p.Status, p.Bytes, p.Comparison.Verdict, p.Comparison.Reason)
		}
	}
	if verdict != nil {
		fmt.Printf("\nverdict: %s\n  %s\n", verdict.Verdict, verdict.Reason)
		if verdict.Verdict == "possible_authz_issue" {
			fmt.Println("  NOT a confirmed finding: an equivalent response is not proof the")
			fmt.Println("  protected resource was returned. Inspect the body and name the resource.")
		}
	}
	return nil
}

func joinNames(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	out := ""
	for i, n := range sorted {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}

// finishDifferentialEvidence seals the operation. Verification answers "did the
// security expectation hold?", so an authorization bypass is recorded as
// unsatisfied, not as a successful operation. A comparison that could not be
// made is "unknown" and is never rounded into either conclusion.
func finishDifferentialEvidence(record *browserOperationEvidence, data map[string]interface{}, verdict string) error {
	if record == nil {
		return nil
	}
	summary := map[string]any{
		"request_id": data["request_id"], "url": data["url"],
		"baseline_samples": data["baseline_samples"],
	}
	if mask, ok := data["volatility"].(volatilityMask); ok {
		summary["mask_kind"] = mask.Kind
		summary["mask_usable"] = mask.Usable
		summary["stable_fields"] = mask.StableCount
		summary["volatile_fields"] = mask.VolatileCount
	}
	verification := "unverified"
	switch verdict {
	case "enforced":
		verification = "satisfied"
	case "possible_authz_issue":
		verification = "unsatisfied"
	case "inconclusive":
		verification = "unknown"
	}
	if verdict != "" {
		summary["authz_verdict"] = verdict
	}
	return record.finish(summary, "completed", verification, "", nil)
}
