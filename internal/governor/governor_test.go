package governor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func newTestGovernor(t *testing.T) *Governor {
	t.Helper()
	g := New(filepath.Join(t.TempDir(), "governor"))
	g.Now = func() time.Time { return fixedNow }
	return g
}

func TestTryLockExcludesSecondHolderInProcess(t *testing.T) {
	g := newTestGovernor(t)
	first, err := g.TryLock("chrome", "reddit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.TryLock("chrome", "reddit"); !errors.Is(err, ErrSiteBusy) {
		t.Fatalf("second holder got %v, want ErrSiteBusy", err)
	}
	other, err := g.TryLock("chrome", "x")
	if err != nil {
		t.Fatalf("different site must not conflict: %v", err)
	}
	other.Release()
	otherBrowser, err := g.TryLock("arc", "reddit")
	if err != nil {
		t.Fatalf("different browser must not conflict: %v", err)
	}
	otherBrowser.Release()
	first.Release()
	first.Release() // idempotent
	again, err := g.TryLock("chrome", "reddit")
	if err != nil {
		t.Fatalf("relock after release: %v", err)
	}
	defer again.Release()
	if filepath.Base(again.Path) != "chrome-reddit.lock" {
		t.Fatalf("lock path = %s", again.Path)
	}
	var nilLock *Lock
	nilLock.Release()
}

func TestFilesAreOwnerOnly(t *testing.T) {
	g := newTestGovernor(t)
	l, err := g.TryLock("Chrome Beta", "../x")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if filepath.Dir(l.Path) != g.Dir || filepath.Base(l.Path) != "chrome_beta-_x.lock" {
		t.Fatalf("unsafe lock path %s", l.Path)
	}
	if err := g.SetCooldown("reddit", fixedNow.Add(time.Minute), "test"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{g.Dir: 0o700, l.Path: 0o600, filepath.Join(g.Dir, "reddit.json"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(g.Dir, ".*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// TestHelperLockHolder is the subprocess side of TestTryLockCrossProcess. It
// holds the lock until its stdin closes.
func TestHelperLockHolder(t *testing.T) {
	if os.Getenv("REP_GOVERNOR_HELPER") != "1" {
		t.Skip("helper for TestTryLockCrossProcess")
	}
	g := New(os.Getenv("REP_GOVERNOR_DIR"))
	l, err := g.TryLock("chrome", "reddit")
	if err != nil {
		fmt.Printf("error %v\n", err)
		os.Exit(2)
	}
	fmt.Println("locked")
	io.ReadAll(os.Stdin)
	l.Release()
	os.Exit(0)
}

func TestTryLockCrossProcess(t *testing.T) {
	g := newTestGovernor(t)
	if err := g.ensureDir(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLockHolder$")
	cmd.Env = append(os.Environ(), "REP_GOVERNOR_HELPER=1", "REP_GOVERNOR_DIR="+g.Dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper did not lock: %q %v", line, err)
	}
	if _, err := g.TryLock("chrome", "reddit"); !errors.Is(err, ErrSiteBusy) {
		t.Fatalf("lock held by another process yielded %v, want ErrSiteBusy", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
	l, err := g.TryLock("chrome", "reddit")
	if err != nil {
		t.Fatalf("lock after helper exit: %v", err)
	}
	l.Release()
}

func TestCooldownRoundTrip(t *testing.T) {
	g := newTestGovernor(t)
	if _, _, active := g.Cooldown("reddit"); active {
		t.Fatal("missing file must be inactive")
	}
	until := fixedNow.Add(90 * time.Second)
	if err := g.SetCooldown("reddit", until, "http 429 retry-after"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(g.Dir, "reddit.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The Python tools parse this exact shape.
	want := `{"until":"2026-11-04T12:01:30Z","reason":"http 429 retry-after","set_at":"2026-11-04T12:00:00Z"}` + "\n"
	if string(raw) != want {
		t.Fatalf("cooldown file\n got %s\nwant %s", raw, want)
	}
	got, reason, active := g.Cooldown("reddit")
	if !active || !got.Equal(until) || reason != "http 429 retry-after" {
		t.Fatalf("Cooldown = %v %q %v", got, reason, active)
	}
	g.Now = func() time.Time { return until.Add(time.Second) }
	if got, _, active := g.Cooldown("reddit"); active || !got.Equal(until) {
		t.Fatal("expired cooldown still active, or its time was lost")
	}
	if err := g.ClearCooldown("reddit"); err != nil {
		t.Fatal(err)
	}
	if err := g.ClearCooldown("reddit"); err != nil {
		t.Fatalf("clearing twice must be fine: %v", err)
	}
	if _, _, active := g.Cooldown("reddit"); active {
		t.Fatal("cleared cooldown still active")
	}
	for name, body := range map[string]string{"garbage": "{not json", "bad time": `{"until":"soon","reason":"","set_at":""}`, "empty": ""} {
		os.WriteFile(filepath.Join(g.Dir, "x.json"), []byte(body), 0o600)
		if _, _, active := g.Cooldown("x"); active {
			t.Errorf("%s file reported active", name)
		}
	}
}

func hdr(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Add(pairs[i], pairs[i+1])
	}
	return h
}

func TestParseRate(t *testing.T) {
	now := fixedNow
	cases := []struct {
		name string
		h    http.Header
		want Rate
	}{
		{"none", hdr("Content-Type", "text/html"), Rate{Limit: -1, Remaining: -1}},
		{"retry-after seconds", hdr("Retry-After", "30"), Rate{HeadersPresent: true, Limit: -1, Remaining: -1, RetryAfter: 30 * time.Second, ResetAt: now.Add(30 * time.Second), Family: "retry-after"}},
		{"retry-after http-date", hdr("Retry-After", now.Add(2*time.Minute).Format(http.TimeFormat)), Rate{HeadersPresent: true, Limit: -1, Remaining: -1, RetryAfter: 2 * time.Minute, ResetAt: now.Add(2 * time.Minute), Family: "retry-after"}},
		{"retry-after past date", hdr("Retry-After", now.Add(-time.Minute).Format(http.TimeFormat)), Rate{HeadersPresent: true, Limit: -1, Remaining: -1, RetryAfter: 0, ResetAt: now.Add(-time.Minute), Family: "retry-after"}},
		{"retry-after junk", hdr("Retry-After", "soon"), Rate{Limit: -1, Remaining: -1}},
		{"ietf item form", hdr("RateLimit", "limit=100, remaining=0, reset=45"), Rate{HeadersPresent: true, Limit: 100, Remaining: 0, ResetAt: now.Add(45 * time.Second), Family: "ratelimit"}},
		{"ietf newer form", hdr("RateLimit", `"default";r=3;t=12`, "RateLimit-Policy", `"default";q=50;w=60`), Rate{HeadersPresent: true, Limit: 50, Remaining: 3, ResetAt: now.Add(12 * time.Second), Family: "ratelimit"}},
		{"ietf legacy trio", hdr("RateLimit-Limit", "60, 60;w=60", "RateLimit-Remaining", "7", "RateLimit-Reset", "9"), Rate{HeadersPresent: true, Limit: 60, Remaining: 7, ResetAt: now.Add(9 * time.Second), Family: "ratelimit"}},
		{"ietf policy quota only", hdr("RateLimit-Policy", "10;w=1"), Rate{HeadersPresent: true, Limit: 10, Remaining: -1, Family: "ratelimit"}},
		{"reddit fractional", hdr("x-ratelimit-limit", "1000", "x-ratelimit-remaining", "996.0", "x-ratelimit-reset", "123.5", "x-ratelimit-used", "4"), Rate{HeadersPresent: true, Limit: 1000, Remaining: 996, ResetAt: now.Add(123500 * time.Millisecond), Family: "x-ratelimit"}},
		{"x epoch", hdr("x-rate-limit-limit", "150", "x-rate-limit-remaining", "12", "x-rate-limit-reset", "1762257600"), Rate{HeadersPresent: true, Limit: 150, Remaining: 12, ResetAt: time.Unix(1762257600, 0), Family: "x-rate-limit"}},
		{"x remaining only", hdr("X-Rate-Limit-Remaining", "0"), Rate{HeadersPresent: true, Limit: -1, Remaining: 0, Family: "x-rate-limit"}},
		{"reddit plus retry-after", hdr("Retry-After", "5", "x-ratelimit-remaining", "0", "x-ratelimit-reset", "20"), Rate{HeadersPresent: true, Limit: -1, Remaining: 0, RetryAfter: 5 * time.Second, ResetAt: now.Add(20 * time.Second), Family: "x-ratelimit"}},
		{"non-canonical keys", http.Header{"x-rate-limit-reset": {"1762257600"}}, Rate{HeadersPresent: true, Limit: -1, Remaining: -1, ResetAt: time.Unix(1762257600, 0), Family: "x-rate-limit"}},
	}
	for _, tc := range cases {
		got := ParseRate(tc.h, now)
		if got.HeadersPresent != tc.want.HeadersPresent || got.Limit != tc.want.Limit || got.Remaining != tc.want.Remaining || got.RetryAfter != tc.want.RetryAfter || got.Family != tc.want.Family || !got.ResetAt.Equal(tc.want.ResetAt) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestObserve(t *testing.T) {
	cases := []struct {
		name   string
		status int
		h      http.Header
		hint   string
		code   string
		until  time.Duration // from fixedNow; 0 = no cooldown
		reason string
	}{
		{"200 carries on", 200, hdr(), "", "", 0, ""},
		{"404 carries on", 404, hdr(), "", "", 0, ""},
		{"429 bare", 429, hdr(), "", "rate_limited", 60 * time.Second, "http 429 default"},
		{"429 retry-after", 429, hdr("Retry-After", "7"), "", "rate_limited", 7 * time.Second, "http 429 retry-after"},
		{"429 retry-after date", 429, hdr("Retry-After", fixedNow.Add(3*time.Minute).Format(http.TimeFormat)), "", "rate_limited", 3 * time.Minute, "http 429 retry-after"},
		{"429 reddit reset rounds up", 429, hdr("x-ratelimit-reset", "42.5"), "", "rate_limited", 43 * time.Second, "http 429 x-ratelimit reset"},
		{"429 x epoch reset", 429, hdr("x-rate-limit-reset", fmt.Sprint(fixedNow.Add(10*time.Minute).Unix())), "", "rate_limited", 10 * time.Minute, "http 429 x-rate-limit reset"},
		{"429 ietf reset", 429, hdr("RateLimit", "limit=5, remaining=0, reset=11"), "", "rate_limited", 11 * time.Second, "http 429 ratelimit reset"},
		{"429 stale reset falls back", 429, hdr("x-rate-limit-reset", "1"), "", "rate_limited", 60 * time.Second, "http 429 default"},
		{"429 absurd retry-after capped", 429, hdr("Retry-After", "999999999"), "", "rate_limited", maxCooldown, "http 429 retry-after"},
		{"401 auth", 401, hdr(), "", "auth", 0, "http 401"},
		{"403 auth", 403, hdr(), "", "auth", 0, "http 403"},
		{"403 cloudflare challenge", 403, hdr("cf-mitigated", "challenge"), "", "challenge", 120 * time.Second, "cf-mitigated challenge"},
		{"200 interstitial body", 200, hdr(), "<title>Prove Your Humanity</title>", "challenge", 120 * time.Second, "prove your humanity"},
		{"429 network security body", 429, hdr(), "You've been blocked by network security.", "challenge", 120 * time.Second, "blocked by network security"},
		{"302 login", 302, hdr("Location", "https://www.reddit.com/login/?dest=x&token=SECRET"), "", "auth", 0, "redirect to /login/"},
		{"302 x flow login", 302, hdr("Location", "/i/flow/login?redirect_after_login=%2Fx"), "", "auth", 0, "redirect to /i/flow/login"},
		{"302 rdt", 302, hdr("Location", "https://www.reddit.com/r/golang/comments/1/?rdt=12345"), "", "auth", 0, "redirect to /r/golang/comments/1/"},
		{"302 elsewhere", 302, hdr("Location", "https://www.reddit.com/r/golang/comments/1/slug/"), "", "", 0, ""},
	}
	for _, tc := range cases {
		g := newTestGovernor(t)
		stop := g.Observe("site", tc.status, tc.h, tc.hint)
		if tc.code == "" {
			if stop != nil {
				t.Errorf("%s: unexpected stop %+v", tc.name, stop)
			}
			if _, _, active := g.Cooldown("site"); active {
				t.Errorf("%s: cooldown written for a non-stop", tc.name)
			}
			continue
		}
		if stop == nil {
			t.Errorf("%s: no stop", tc.name)
			continue
		}
		if stop.Code != tc.code || stop.Reason != tc.reason {
			t.Errorf("%s: stop = %+v", tc.name, stop)
		}
		if strings.Contains(stop.Reason, "SECRET") {
			t.Errorf("%s: redirect query leaked into reason", tc.name)
		}
		until, _, active := g.Cooldown("site")
		if tc.until == 0 {
			if !stop.Until.IsZero() || active {
				t.Errorf("%s: cooldown set for %s", tc.name, tc.code)
			}
			continue
		}
		if !stop.Until.Equal(fixedNow.Add(tc.until)) || !active || !until.Equal(stop.Until) {
			t.Errorf("%s: until = %v (file %v active=%v), want +%v", tc.name, stop.Until, until, active, tc.until)
		}
	}
}

func TestObserveNeverShortensCooldown(t *testing.T) {
	g := newTestGovernor(t)
	long := fixedNow.Add(10 * time.Minute)
	if err := g.SetCooldown("site", long, "earlier"); err != nil {
		t.Fatal(err)
	}
	stop := g.Observe("site", 429, hdr("Retry-After", "5"), "")
	if !stop.Until.Equal(long) {
		t.Fatalf("cooldown shortened to %v", stop.Until)
	}
	if until, _, _ := g.Cooldown("site"); !until.Equal(long) {
		t.Fatalf("file shortened to %v", until)
	}
	// A longer new cooldown does replace the shorter old one.
	stop = g.Observe("site", 429, hdr("Retry-After", "3600"), "")
	if !stop.Until.Equal(fixedNow.Add(time.Hour)) {
		t.Fatalf("longer cooldown not applied: %v", stop.Until)
	}
}

func TestObserveReportsPersistenceFailure(t *testing.T) {
	g := New(filepath.Join(t.TempDir(), "file-not-dir", "governor"))
	g.Now = func() time.Time { return fixedNow }
	os.WriteFile(filepath.Dir(g.Dir), []byte("x"), 0o600)
	stop := g.Observe("site", 429, hdr(), "")
	if stop == nil || stop.Code != "rate_limited" || !strings.Contains(stop.Reason, "cooldown not persisted") {
		t.Fatalf("stop = %+v", stop)
	}
}

func TestPacerSlots(t *testing.T) {
	g := newTestGovernor(t)
	clock := fixedNow
	g.Now = func() time.Time { return clock }
	p := g.Pacer(800 * time.Millisecond)
	var slept []time.Duration
	// The stub advances the clock like a real sleep would, so the re-check loop
	// terminates and the recorded durations are exactly the floors enforced.
	p.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		clock = clock.Add(d)
		return nil
	}
	ctx := context.Background()
	p.Wait(ctx)                               // first return is immediate: no sleep
	p.Wait(ctx)                               // same instant: full floor
	clock = clock.Add(2 * time.Second)        // caller returns long after its slot
	p.Wait(ctx)                               // -> immediate
	clock = clock.Add(300 * time.Millisecond) // 300 ms into the floor
	p.Wait(ctx)                               // -> 500 ms remaining
	want := []time.Duration{800 * time.Millisecond, 500 * time.Millisecond}
	if len(slept) != len(want) {
		t.Fatalf("sleeps = %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", slept, want)
		}
	}
	// Floor of zero never sleeps.
	z := g.Pacer(0)
	z.sleep = func(_ context.Context, d time.Duration) error {
		t.Fatalf("zero floor slept %v", d)
		return nil
	}
	z.Wait(ctx)
	z.Wait(ctx)
}

func TestPacerHonoursContext(t *testing.T) {
	g := newTestGovernor(t)
	p := g.Pacer(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait returned %v", err)
	}
	// A live context with a real timer: the sleep ends when the deadline hits.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Millisecond)
	defer cancel2()
	if err := p.Wait(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline wait returned %v", err)
	}
}

func TestPacerRealTimerKeepsFloor(t *testing.T) {
	g := New(t.TempDir())
	const floor = 2 * time.Millisecond
	p := g.Pacer(floor)
	start := time.Now()
	var mu sync.Mutex
	var returns []time.Time
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Wait(context.Background()); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			returns = append(returns, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(returns) != 4 {
		t.Fatalf("returns = %d", len(returns))
	}
	sort.Slice(returns, func(i, j int) bool { return returns[i].Before(returns[j]) })
	// Slots are at least start, start+floor, +2 floors, +3 floors. A return may
	// be late (scheduling) but never earlier than its slot, so the k-th earliest
	// return is at or after start + k*floor regardless of timer jitter.
	for k, r := range returns {
		if earliest := start.Add(time.Duration(k) * floor); r.Before(earliest) {
			t.Fatalf("return %d at +%v precedes its slot at +%v", k, r.Sub(start), earliest.Sub(start))
		}
	}
}

// Mirrors python/test_governor.py: both writers of <site>.json keep the longer
// active cooldown and cap at 24 h, so neither tool can undo the other's wait.
func TestSetCooldownSharesWriterRules(t *testing.T) {
	g := newTestGovernor(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(g.SetCooldown("facebook.com", fixedNow.Add(300*time.Second), "first"))
	must(g.SetCooldown("facebook.com", fixedNow.Add(30*time.Second), "second"))
	if until, reason, _ := g.Cooldown("facebook.com"); reason != "first" || !until.Equal(fixedNow.Add(300*time.Second)) {
		t.Fatalf("shorter cooldown replaced the longer one: %v %q", until, reason)
	}
	must(g.SetCooldown("facebook.com", fixedNow.Add(600*time.Second), "third"))
	if _, reason, _ := g.Cooldown("facebook.com"); reason != "third" {
		t.Fatalf("longer cooldown not applied: %q", reason)
	}
	must(g.SetCooldown("reddit.com", fixedNow.Add(30*24*time.Hour), "Retry-After: a month"))
	if until, _, _ := g.Cooldown("reddit.com"); !until.Equal(fixedNow.Add(maxCooldown)) {
		t.Fatalf("cap not applied: %v", until)
	}
	// An expired cooldown never blocks a new, shorter one.
	g.Now = func() time.Time { return fixedNow.Add(time.Hour) }
	must(g.SetCooldown("facebook.com", fixedNow.Add(time.Hour+time.Minute), "fresh"))
	if _, reason, active := g.Cooldown("facebook.com"); !active || reason != "fresh" {
		t.Fatalf("expired cooldown blocked a new one: %q %v", reason, active)
	}
}

func TestCooldownFileIsPlainJSONForPython(t *testing.T) {
	g := newTestGovernor(t)
	if err := g.SetCooldown("facebook", fixedNow.Add(time.Minute), "rate error"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(g.Dir, "facebook.json"))
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"until", "reason", "set_at"} {
		if _, ok := doc[key].(string); !ok {
			t.Fatalf("key %s missing or not a string: %v", key, doc)
		}
	}
	if len(doc) != 3 {
		t.Fatalf("unexpected keys: %v", doc)
	}
}
