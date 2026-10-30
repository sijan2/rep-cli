// Package agentstate owns the machine-wide handle index. All changes are atomic
// under flock; browser RPCs never run while this lock is held.
package agentstate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/repplus/rep-cli/internal/scope"
)

const TabCap = 16

var TabPattern = regexp.MustCompile(`^t[1-9][0-9]*$`)
var RefPattern = regexp.MustCompile(`^(t[1-9][0-9]*)\.e[1-9][0-9]*$`)
var CursorPattern = regexp.MustCompile(`^p_[a-f0-9]{32}$`)

type Error struct {
	Code, Message, Next, Retry string
	Candidates                 []string
}

func (e *Error) Error() string { return e.Message }
func Failure(code, message, next string) *Error {
	return &Error{Code: code, Message: message, Next: next, Retry: "needs-agent"}
}

type Browser struct {
	Name        string `json:"name"`
	Profile     string `json:"profile_id"`
	Epoch       string `json:"epoch"`
	PrivateName string `json:"private_name,omitempty"`
}
type Lease struct {
	ID      string    `json:"id"`
	Until   time.Time `json:"until"`
	Purpose string    `json:"purpose"`
}
type Ref struct {
	Raw        string    `json:"raw"`
	Generation string    `json:"generation"`
	At         time.Time `json:"at"`
}
type Tab struct {
	TransportOrigin string         `json:"transport_origin,omitempty"`
	Handle          string         `json:"handle"`
	Browser         Browser        `json:"browser"`
	ChromeID        int            `json:"chrome_id"`
	Owner           string         `json:"owner"`
	Session         string         `json:"session,omitempty"`
	Created         time.Time      `json:"created"`
	LastUsed        time.Time      `json:"last_used"`
	CreatedByRep    bool           `json:"created_by_rep"`
	Closed          bool           `json:"closed,omitempty"`
	URL             string         `json:"url,omitempty"`
	Generation      string         `json:"generation,omitempty"`
	NextRef         uint64         `json:"next_ref"`
	Refs            map[string]Ref `json:"refs,omitempty"`
	Lease           *Lease         `json:"lease,omitempty"`
	Viewport        string         `json:"viewport,omitempty"`
	HasInput        bool           `json:"has_input,omitempty"`
	Dialog          bool           `json:"dialog,omitempty"`
}

func (t *Tab) Busy(now time.Time) bool { return t.Lease != nil && t.Lease.Until.After(now) }

type Cursor struct {
	ID       string    `json:"id"`
	Tab      string    `json:"tab"`
	Owner    string    `json:"owner"`
	Kind     string    `json:"kind"`
	URL      string    `json:"url,omitempty"`
	Digest   string    `json:"digest,omitempty"`
	Offset   int       `json:"offset"`
	Controls int       `json:"controls"`
	Budget   int       `json:"budget"`
	Attempt  int       `json:"attempt"`
	Created  time.Time `json:"created"`
	Selector string    `json:"selector,omitempty"`
	Section  string    `json:"section,omitempty"`
	Find     string    `json:"find,omitempty"`
	Source   string    `json:"source,omitempty"`
}
type Capture struct {
	Browser Browser   `json:"browser"`
	ID      string    `json:"id"`
	Owner   string    `json:"owner"`
	Archive string    `json:"archive"`
	Request string    `json:"request,omitempty"`
	Tab     string    `json:"tab,omitempty"`
	Created time.Time `json:"created"`
}
type Task struct {
	Browser         *Browser             `json:"browser,omitempty"`
	Sessions        map[string]time.Time `json:"sessions,omitempty"`
	BridgeDownSince *time.Time           `json:"bridge_down_since,omitempty"`
}
type State struct {
	Version  int                `json:"version"`
	NextTab  uint64             `json:"next_tab"`
	Tabs     map[string]*Tab    `json:"tabs"`
	Cursors  map[string]Cursor  `json:"cursors"`
	Tasks    map[string]*Task   `json:"tasks"`
	Captures map[string]Capture `json:"captures"`
}
type Store struct {
	Root string
	Now  func() time.Time
}

func Default() (*Store, error) { root, err := scope.GlobalDataDir(); return &Store{Root: root}, err }
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}
func empty() *State {
	return &State{Version: 1, Tabs: map[string]*Tab{}, Cursors: map[string]Cursor{}, Tasks: map[string]*Task{}, Captures: map[string]Capture{}}
}
func (s *Store) read() (*State, error) {
	data, err := os.ReadFile(filepath.Join(s.Root, "handles.json"))
	if os.IsNotExist(err) {
		return empty(), nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 {
		return nil, errors.New("handle registry exceeds 64 MiB")
	}
	state := empty()
	if err := json.Unmarshal(data, state); err != nil {
		return nil, errors.New("handle registry is corrupt; refusing to replace it")
	}
	if state.Version != 1 || state.Tabs == nil || state.Tasks == nil || state.Cursors == nil || state.Captures == nil {
		return nil, errors.New("unsupported handle registry")
	}
	return state, nil
}
func (s *Store) Read() (*State, error) {
	lock, err := os.OpenFile(filepath.Join(s.Root, "handles.lock"), os.O_RDONLY, 0)
	if os.IsNotExist(err) {
		return s.read()
	}
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_SH); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return s.read()
}
func (s *Store) Update(fn func(*State) error) error {
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.Root, "handles.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state, err := s.read()
	if err != nil {
		return err
	}
	if err = fn(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.Root, ".handles-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(s.Root, "handles.json"))
}
func Token(prefix string) string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(data[:])
}
func TabOf(handle string) string {
	if TabPattern.MatchString(handle) {
		return handle
	}
	if found := RefPattern.FindStringSubmatch(handle); found != nil {
		return found[1]
	}
	return ""
}
func (s *Store) Owner(handle string) (string, error) {
	state, err := s.Read()
	if err != nil {
		return "", err
	}
	if tab := state.Tabs[TabOf(handle)]; tab != nil {
		return tab.Owner, nil
	}
	if cursor, ok := state.Cursors[handle]; ok {
		return cursor.Owner, nil
	}
	if capture, ok := state.Captures[handle]; ok {
		return capture.Owner, nil
	}
	return "", Failure("handle_not_found", "unknown handle", "rep tabs")
}
func (s *Store) Tab(handle string) (*Tab, error) {
	state, err := s.Read()
	if err != nil {
		return nil, err
	}
	tab := state.Tabs[TabOf(handle)]
	if tab == nil {
		return nil, Failure("tab_gone", "tab handle is unavailable", "rep tabs")
	}
	if tab.Closed {
		return tab, Failure("tab_gone", "tab has closed", "rep tabs")
	}
	return tab, nil
}
func task(state *State, name string) *Task {
	value := state.Tasks[name]
	if value == nil {
		value = &Task{Sessions: map[string]time.Time{}}
		state.Tasks[name] = value
	}
	if value.Sessions == nil {
		value.Sessions = map[string]time.Time{}
	}
	return value
}
func (s *Store) Remember(owner, session string, browser Browser) error {
	return s.Update(func(state *State) error {
		value := task(state, owner)
		for _, tab := range state.Tabs {
			if tab.Owner == owner && !tab.Closed && (tab.Browser.Profile != browser.Profile || tab.Browser.Epoch != browser.Epoch) {
				return Failure("browser_in_use", "task still owns tabs in another browser", "rep tabs")
			}
		}
		value.Browser = &browser
		if session != "" {
			value.Sessions[session] = s.now()
		}
		return nil
	})
}
func (s *Store) Remembered(owner string) (*Browser, error) {
	state, err := s.Read()
	if err != nil {
		return nil, err
	}
	if value := state.Tasks[owner]; value != nil {
		return value.Browser, nil
	}
	return nil, nil
}
func sameBrowser(a, b Browser) bool { return a.Profile == b.Profile && a.Epoch == b.Epoch }
func (s *Store) Register(browser Browser, chromeID int, owner, session string, created bool) (*Tab, error) {
	var result *Tab
	err := s.Update(func(state *State) error {
		if browser.Profile == "" || browser.Epoch == "" {
			return Failure("bridge_outdated", "bridge has no profile identity", "rep doctor")
		}
		for _, tab := range state.Tabs {
			if sameBrowser(tab.Browser, browser) && tab.ChromeID == chromeID && !tab.Closed {
				if tab.Owner != owner {
					return Failure("owned_by_other_task", "tab belongs to another task", "rep tabs")
				}
				result = tab
				return nil
			}
		}
		count := 0
		for _, t := range state.Tabs {
			if t.Owner == owner && sameBrowser(t.Browser, browser) && !t.Closed {
				count++
			}
		}
		if count >= TabCap {
			return Failure("tab_cap", "task tab cap reached; close an idle tab", "rep tabs")
		}
		state.NextTab++
		result = &Tab{Handle: fmt.Sprintf("t%d", state.NextTab), Browser: browser, ChromeID: chromeID, Owner: owner, Session: session, Created: s.now(), LastUsed: s.now(), CreatedByRep: created, Refs: map[string]Ref{}}
		state.Tabs[result.Handle] = result
		return nil
	})
	return result, err
}

// Reserve selects and locks a target before navigation. New tabs are reserved
// with ChromeID=-1 before creation so concurrent callers cannot clobber a tab.
func (s *Store) Reserve(browser Browser, owner, session, explicit string, pinned, navigate bool, duration time.Duration) (*Tab, string, error) {
	var result *Tab
	lease := Token("l_")
	err := s.Update(func(state *State) error {
		now := s.now()
		selected := task(state, owner)
		shared := false
		for key, seen := range selected.Sessions {
			if now.Sub(seen) > 10*time.Minute {
				delete(selected.Sessions, key)
			} else if key != session {
				shared = true
			}
		}
		if session != "" {
			selected.Sessions[session] = now
		}
		if explicit != "" {
			result = state.Tabs[TabOf(explicit)]
			if result == nil || result.Closed {
				return Failure("tab_gone", "tab has closed", "rep tabs")
			}
			if result.Owner != owner {
				return Failure("owned_by_other_task", "explicit task does not own this tab", "rep tabs")
			}
			if !sameBrowser(result.Browser, browser) {
				return Failure("tab_gone", "tab belongs to a previous browser session", "rep tabs")
			}
			if result.Busy(now) {
				return &Error{Code: "tab_busy", Message: "tab has an active execute lease", Next: "rep page " + result.Handle, Retry: "retryable"}
			}
		} else {
			var candidates []*Tab
			for _, tab := range state.Tabs {
				if tab.Owner == owner && !tab.Closed && sameBrowser(tab.Browser, browser) && tab.TransportOrigin == "" {
					candidates = append(candidates, tab)
				}
			}
			sort.Slice(candidates, func(i, j int) bool { return candidates[i].LastUsed.After(candidates[j].LastUsed) })
			if navigate && len(candidates) == 0 { /* allocate below */
			} else {
				if shared && navigate && !pinned {
					return Failure("task_shared", "task is used by another agent session; address a handle", "rep tabs")
				}
				// A derived session cannot distinguish its same-session siblings. Reuse and
				// other implicit writes require a pin or a handle, even with one tab.
				if navigate && !pinned && strings.HasPrefix(session, "agent-") && len(candidates) > 0 {
					return Failure("ambiguous_target", "address a tab before navigating a shared derived task", "rep tabs")
				}
				recent := []*Tab{}
				for _, tab := range candidates {
					if now.Sub(tab.LastUsed) < 10*time.Minute || tab.Busy(now) {
						recent = append(recent, tab)
					}
				}
				if len(recent) > 1 || len(recent) == 1 && recent[0].Busy(now) && !navigate {
					e := Failure("ambiguous_target", "multiple recent tabs or a leased tab require a handle", "rep tabs")
					for _, t := range recent {
						e.Candidates = append(e.Candidates, t.Handle)
					}
					return e
				}
				if len(recent) == 1 && !recent[0].Busy(now) {
					result = recent[0]
				} else if len(recent) == 0 && len(candidates) == 1 && !candidates[0].Busy(now) {
					result = candidates[0]
				}
				if result == nil && !navigate {
					return Failure("ambiguous_target", "select a tab handle", "rep tabs")
				}
			}
			if result == nil {
				count := 0
				for _, t := range state.Tabs {
					if t.Owner == owner && !t.Closed && sameBrowser(t.Browser, browser) {
						count++
					}
				}
				if count >= TabCap {
					return Failure("tab_cap", "task tab cap reached; close an idle tab", "rep tabs")
				}
				state.NextTab++
				result = &Tab{Handle: fmt.Sprintf("t%d", state.NextTab), Browser: browser, ChromeID: -1, Owner: owner, Session: session, Created: now, CreatedByRep: true, Refs: map[string]Ref{}}
				state.Tabs[result.Handle] = result
			}
		}
		result.LastUsed = now
		result.Lease = &Lease{ID: lease, Until: now.Add(duration), Purpose: "execute"}
		return nil
	})
	return result, lease, err
}
func (s *Store) Change(handle, lease string, fn func(*Tab) error) error {
	return s.Update(func(state *State) error {
		tab := state.Tabs[handle]
		if tab == nil {
			return Failure("tab_gone", "tab is unavailable", "rep tabs")
		}
		if lease != "" && (tab.Lease == nil || tab.Lease.ID != lease) {
			return Failure("lease_lost", "tab lease changed", "rep page "+handle)
		}
		return fn(tab)
	})
}
func (s *Store) Release(handle, lease string) error {
	return s.Change(handle, lease, func(tab *Tab) error { tab.Lease = nil; tab.LastUsed = s.now(); return nil })
}
func (s *Store) MintRefs(handle, lease, generation string, raw []string) ([]string, error) {
	var refs []string
	err := s.Change(handle, lease, func(tab *Tab) error {
		if tab.Generation != generation {
			tab.Refs = map[string]Ref{}
			tab.Generation = generation
		}
		if tab.Refs == nil {
			tab.Refs = map[string]Ref{}
		}
		for key, ref := range tab.Refs {
			if s.now().Sub(ref.At) > 120*time.Second {
				delete(tab.Refs, key)
			}
		}
		for _, value := range raw {
			tab.NextRef++
			name := fmt.Sprintf("%s.e%d", handle, tab.NextRef)
			tab.Refs[name] = Ref{value, generation, s.now()}
			refs = append(refs, name)
		}
		return nil
	})
	return refs, err
}
func (s *Store) ResolveRef(ref string) (Ref, error) {
	tab, err := s.Tab(ref)
	if err != nil {
		return Ref{}, err
	}
	value, ok := tab.Refs[ref]
	if !ok || s.now().Sub(value.At) > 120*time.Second || value.Generation != tab.Generation {
		return Ref{}, Failure("reference_stale", "control reference expired or its document changed", "rep page "+tab.Handle)
	}
	return value, nil
}
func (s *Store) SaveCursor(cursor Cursor) (string, error) {
	cursor.ID = Token("p_")
	cursor.Created = s.now()
	err := s.Update(func(state *State) error {
		state.Cursors[cursor.ID] = cursor
		for key, value := range state.Cursors {
			if s.now().Sub(value.Created) > 7*24*time.Hour {
				delete(state.Cursors, key)
			}
		}
		return nil
	})
	return cursor.ID, err
}
func (s *Store) Cursor(handle string) (Cursor, error) {
	state, err := s.Read()
	if err != nil {
		return Cursor{}, err
	}
	value, ok := state.Cursors[handle]
	if !ok {
		return Cursor{}, Failure("cursor_not_found", "cursor is unavailable", "rep tabs")
	}
	return value, nil
}
func (s *Store) OtherHolders(profile, owner string) ([]string, error) {
	state, err := s.Read()
	if err != nil {
		return nil, err
	}
	var handles []string
	for _, tab := range state.Tabs {
		if tab.Browser.Profile == profile && tab.Owner != owner && !tab.Closed {
			handles = append(handles, tab.Handle)
		}
	}
	sort.Strings(handles)
	return handles, nil
}

// ObserveEpoch invalidates handles from a previous browser session. A bridge
// reconnect keeps the epoch and therefore leaves every handle intact.
func (s *Store) ObserveEpoch(profile, epoch string) error {
	return s.Update(func(state *State) error {
		for _, tab := range state.Tabs {
			if tab.Browser.Profile == profile && tab.Browser.Epoch != epoch {
				tab.Closed = true
				tab.Lease = nil
				tab.Refs = nil
			}
		}
		return nil
	})
}
