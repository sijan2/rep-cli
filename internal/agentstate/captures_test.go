package agentstate

import (
	"sync"
	"testing"
	"time"
)

func TestTransportReservationDoesNotReplaceForeground(t *testing.T) {
	s := fixtureStore(t)
	browser := fixtureBrowser()
	page, err := s.Register(browser, 7, "default/fixture", "session", true)
	if err != nil {
		t.Fatal(err)
	}
	transport, lease, err := s.ReserveTransport(browser, page.Owner, "session", "https://example.test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if page.Handle == transport.Handle {
		t.Fatal("foreground selected for transport")
	}
	if _, _, err = s.ReserveTransport(browser, page.Owner, "session", "https://example.test", time.Minute); err == nil {
		t.Fatal("concurrent transport did not refuse")
	}
	if err = s.Release(transport.Handle, lease); err != nil {
		t.Fatal(err)
	}
	again, lease, err := s.ReserveTransport(browser, page.Owner, "session", "https://example.test", time.Minute)
	if err != nil || again.Handle != transport.Handle {
		t.Fatalf("reuse: %v %v", again, err)
	}
	s.Release(again.Handle, lease)
	selected, lease, err := s.Reserve(browser, page.Owner, "session", "", false, false, time.Minute)
	if err != nil || selected.Handle != page.Handle {
		t.Fatalf("implicit page: %v %v", selected, err)
	}
	s.Release(selected.Handle, lease)
}
func TestTransportConcurrentClaimAndTaskIsolation(t *testing.T) {
	s := fixtureStore(t)
	browser := fixtureBrowser()
	var wg sync.WaitGroup
	won := make(chan *Tab, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tab, _, err := s.ReserveTransport(browser, "default/a", "session", "https://example.test", time.Minute)
			if err == nil {
				won <- tab
			}
		}()
	}
	wg.Wait()
	close(won)
	if len(won) != 1 {
		t.Fatalf("winners %d", len(won))
	}
	other, _, err := s.ReserveTransport(browser, "default/b", "session", "https://example.test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for tab := range won {
		if tab.Handle == other.Handle {
			t.Fatal("tasks share transport")
		}
	}
}
func TestTransportCountsTowardForegroundCap(t *testing.T) {
	s := fixtureStore(t)
	b := fixtureBrowser()
	for i := 0; i < TabCap; i++ {
		tab, lease, err := s.ReserveTransport(b, "default/a", "session", "https://host"+string(rune('a'+i))+".test", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		s.Release(tab.Handle, lease)
	}
	_, _, err := s.Reserve(b, "default/a", "session", "", true, true, time.Minute)
	requireCode(t, err, "tab_cap")
}
func TestCaptureExactArchiveAndRecentAmbiguity(t *testing.T) {
	s := fixtureStore(t)
	if err := s.RegisterCapture("s_one", "default/a", "", "one", []string{"h_one"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterCapture("s_two", "default/a", "", "two", []string{"h_two"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Capture("", "default/a")
	requireCode(t, err, "ambiguous_target")
	capture, err := s.Capture("h_one", "default/a")
	if err != nil || capture.Archive != "one" {
		t.Fatalf("%v %v", capture, err)
	}
	_, err = s.Capture("h_missing", "default/a")
	requireCode(t, err, "capture_not_found")
	if err = s.RegisterCapture("s_three", "default/a", "", "three", []string{"h_one"}); err == nil {
		t.Fatal("duplicate request accepted")
	}
}
