package commands

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/store"
)

func TestCoreReplayWriteGuardAndMutations(t *testing.T) {
	source := &store.Request{Method: "POST", URL: "https://api.test/data?q=one", PageURL: "https://page.test/view", Headers: store.HeaderMap{"Content-Type": {"application/json"}, "Cookie": {"fake"}}, Body: `{"value":1}`, RequestBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 11}}
	if _, _, err := coreReplayVariants(source, nil, "", false); err == nil {
		t.Fatal("unsafe source accepted")
	}
	if _, _, err := coreReplayVariants(source, []string{"method=GET"}, "", false); err == nil {
		t.Fatal("unsafe source downgraded without guard")
	}
	source.Method = "GET"
	if _, _, err := coreReplayVariants(source, []string{"method=PATCH"}, "", false); err == nil {
		t.Fatal("unsafe rewrite accepted")
	}
	variants, origin, err := coreReplayVariants(source, []string{"header.X-Test=fixture"}, `query.q=["a,b","c"]`, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 2 || origin != "https://page.test" || !strings.Contains(variants[0].URL, "a%2Cb") || variants[0].Headers["cookie"] != "" {
		t.Fatalf("%+v %s", variants, origin)
	}
	if source.URL != "https://api.test/data?q=one" {
		t.Fatal("source mutated")
	}
}
func runCoreTransportJS(t *testing.T, method, fixture string, values int) map[string]any {
	t.Helper()
	variants := []fanoutVariant{}
	for i := 0; i < values; i++ {
		variants = append(variants, fanoutVariant{Index: i, Method: method, URL: "https://example.test/"})
	}
	script, err := coreTransportScript(variants, 5*time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	program := fixture + "\nPromise.resolve(" + script + ").then(result=>process.stdout.write(JSON.stringify({result,calls,peak,seen}))).catch(e=>{process.stderr.write(String(e));process.exit(1)});"
	command := exec.Command("node", "-e", program)
	bytes, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v %s", err, bytes)
	}
	var result map[string]any
	if err = json.Unmarshal(bytes, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

const coreFetchFixture = `let calls=0,active=0,peak=0,seen=[];
const response=status=>({status,ok:status>=200&&status<300,headers:new Map(),body:null});`

func TestCoreTransportWriteStopsWithoutRetry(t *testing.T) {
	result := runCoreTransportJS(t, "POST", coreFetchFixture+`global.fetch=async(url,init)=>{calls++;seen.push(init);return response(403)};`, 5)
	if result["calls"] != float64(1) {
		t.Fatalf("write retried or continued: %v", result)
	}
	seen := result["seen"].([]any)[0].(map[string]any)
	if seen["credentials"] != "include" || seen["cache"] != "no-store" || seen["redirect"] != "error" {
		t.Fatal(seen)
	}
}
func TestCoreTransportSafeParallelAnd429Backoff(t *testing.T) {
	result := runCoreTransportJS(t, "GET", coreFetchFixture+`global.fetch=async(url,init)=>{calls++;active++;peak=Math.max(peak,active);const n=calls;await new Promise(r=>setTimeout(r,10));active--;return response(n===1?429:200)};`, 8)
	if result["calls"] != float64(9) || result["peak"].(float64) > 4 || result["peak"].(float64) < 2 {
		t.Fatal(result)
	}
	if result["result"].(map[string]any)["retries"] != float64(1) {
		t.Fatal(result)
	}
}
func TestCoreTransport403StopsSafeUnstarted(t *testing.T) {
	result := runCoreTransportJS(t, "GET", coreFetchFixture+`global.fetch=async()=>{calls++;return response(403)};`, 10)
	if result["calls"] != float64(4) {
		t.Fatalf("403 retried or more batches started: %v", result)
	}
}
func TestCoreTransportTimeoutDoesNotRetryWrite(t *testing.T) {
	result := runCoreTransportJS(t, "POST", coreFetchFixture+`global.fetch=async()=>{calls++;throw Error('fake transport failure')};`, 5)
	if result["calls"] != float64(1) {
		t.Fatal(result)
	}
}

func TestCoreTransportConsumesActualLargeBody(t *testing.T) {
	result := runCoreTransportJS(t, "GET", coreFetchFixture+`global.fetch=async()=>{calls++;return new Response(new Uint8Array((8<<20)+257),{headers:{'content-length':'1'}})};`, 1)
	variant := result["result"].(map[string]any)["variants"].([]any)[0].(map[string]any)
	if variant["ok"] != true || variant["bytes"] != float64((8<<20)+257) || variant["error"] != nil {
		t.Fatalf("body consumption relied on headers instead of actual bytes: %v", variant)
	}
}

func TestCoreTransportBodyFailureStaysFailedWithoutWriteRetry(t *testing.T) {
	result := runCoreTransportJS(t, "POST", coreFetchFixture+`global.fetch=async()=>{calls++;return new Response(new ReadableStream({start(c){c.enqueue(new Uint8Array(1024));setTimeout(()=>c.error(Error('fixture disconnect')),1)}}))};`, 3)
	variant := result["result"].(map[string]any)["variants"].([]any)[0].(map[string]any)
	if result["calls"] != float64(1) || variant["ok"] != false || variant["error"] != "request_failed" {
		t.Fatalf("failed body was counted complete or unsafe request repeated: %v", result)
	}
}
func TestCoreDownloadInferredValidation(t *testing.T) {
	if coreDownloadValidation("report.PDF").ExpectedMagic != "pdf" || coreDownloadValidation("file.txt").ExpectedMagic != "" {
		t.Fatal("extension inference")
	}
}

func TestCoreOriginNormalizesBrowserOrigin(t *testing.T) {
	for input, want := range map[string]string{"http://EXAMPLE.test:80/a": "http://example.test", "https://EXAMPLE.test:443/a": "https://example.test", "http://[::1]:8080/a": "http://[::1]:8080"} {
		got, err := coreRequestURL(input)
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", input, got, err)
		}
	}
}

func TestCoreReplayHeaderMutationReplacesCaseInsensitively(t *testing.T) {
	source := &store.Request{Method: "GET", URL: "https://example.test/", Headers: store.HeaderMap{"X-Test": {"old"}}}
	variants, _, err := coreReplayVariants(source, []string{"header.x-test=new"}, "", false)
	if err != nil || variants[0].Headers["x-test"] != "new" || len(variants[0].Headers) != 1 {
		t.Fatalf("%v %v", variants, err)
	}
	if source.Headers["X-Test"][0] != "old" {
		t.Fatal("source headers mutated")
	}
}
