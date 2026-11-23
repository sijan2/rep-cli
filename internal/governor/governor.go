// Package governor is the cross-process rate and stop policy for site runs: an
// exclusive per-site lock, persisted cooldowns that every process (including
// the Python tools) honors, a fixed-floor pacer, and rate-limit header parsing.
// It never retries and never sleeps on a caller's behalf outside Pacer.Wait.
package governor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/repplus/rep-cli/internal/scope"
)

var ErrSiteBusy = errors.New("site_busy")

var errLockHeld = errors.New("lock held")

// Governor keeps its state under Dir. Now is injectable so cooldown arithmetic
// is testable without sleeping; nil means time.Now.
type Governor struct {
	Dir string
	Now func() time.Time
}

func New(dir string) *Governor { return &Governor{Dir: dir, Now: time.Now} }

// DefaultDir is the shared location every rep process and tool reads.
func DefaultDir() (string, error) {
	base, err := scope.GlobalDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "governor"), nil
}

func (g *Governor) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Governor) ensureDir() error {
	if g.Dir == "" {
		return errors.New("governor: no directory configured")
	}
	return os.MkdirAll(g.Dir, 0o700)
}

// safeName confines browser and site names to one path component so a caller
// can never address a file outside Dir.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "_"
	}
	return out
}

// Lock is an exclusive per-(browser, site) run lock held until Release.
type Lock struct {
	Path string
	file *os.File
	once sync.Once
}

// TryLock takes <browser>-<site>.lock without blocking. ErrSiteBusy (wrapped)
// means another run, in this or any other process, holds it. The lock file is
// never removed: unlinking a live lock file lets a third process lock a fresh
// inode and defeats the exclusion.
func (g *Governor) TryLock(browser, site string) (*Lock, error) {
	if err := g.ensureDir(); err != nil {
		return nil, err
	}
	path := filepath.Join(g.Dir, safeName(browser)+"-"+safeName(site)+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := tryLockFile(f); err != nil {
		f.Close()
		if errors.Is(err, errLockHeld) {
			return nil, fmt.Errorf("%w: %s is held by another run", ErrSiteBusy, filepath.Base(path))
		}
		return nil, err
	}
	return &Lock{Path: path, file: f}, nil
}

// Release is idempotent and safe on a nil Lock.
func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	l.once.Do(func() {
		unlockFile(l.file)
		l.file.Close()
	})
}

// cooldownFile is the on-disk format shared with the Python tools.
type cooldownFile struct {
	Until  string `json:"until"`
	Reason string `json:"reason"`
	SetAt  string `json:"set_at"`
}

func (g *Governor) cooldownPath(site string) string {
	return filepath.Join(g.Dir, safeName(site)+".json")
}

// Cooldown reads <site>.json. A missing or unreadable file is inactive: writes
// are atomic, so a corrupt file means a hand edit, and a cooldown nobody can
// see the reason for should not block every future run.
func (g *Governor) Cooldown(site string) (until time.Time, reason string, active bool) {
	data, err := os.ReadFile(g.cooldownPath(site))
	if err != nil {
		return time.Time{}, "", false
	}
	var f cooldownFile
	if err := json.Unmarshal(data, &f); err != nil {
		return time.Time{}, "", false
	}
	until, err = time.Parse(time.RFC3339, f.Until)
	if err != nil {
		return time.Time{}, "", false
	}
	return until, f.Reason, until.After(g.now())
}

// ceilSecond rounds up to the whole second the file format can carry, so the
// persisted cooldown is never shorter than the one a caller was told about.
func ceilSecond(t time.Time) time.Time {
	if whole := t.Truncate(time.Second); whole.Before(t) {
		return whole.Add(time.Second)
	}
	return t
}

// A malformed far-future Retry-After must not lock a site out for good; the
// Python writer applies the same ceiling.
const maxCooldown = 24 * time.Hour

// clampCooldown applies the rules both writers of <site>.json share: whole
// seconds, at most 24 h from now, and never shorter than an active cooldown
// (floors only rise; ClearCooldown is the explicit reset).
func (g *Governor) clampCooldown(site string, until time.Time) time.Time {
	until = ceilSecond(until)
	if limit := ceilSecond(g.now().Add(maxCooldown)); until.After(limit) {
		until = limit
	}
	if existing, _, active := g.Cooldown(site); active && existing.After(until) {
		until = existing
	}
	return until
}

// SetCooldown writes <site>.json atomically (temp file + rename) so readers in
// other processes never observe a partial document. When an active cooldown
// already reaches further, the file is left as it is, reason included.
func (g *Governor) SetCooldown(site string, until time.Time, reason string) error {
	if err := g.ensureDir(); err != nil {
		return err
	}
	clamped := g.clampCooldown(site, until)
	if existing, _, active := g.Cooldown(site); active && !clamped.After(existing) {
		return nil
	}
	until = clamped
	data, err := json.Marshal(cooldownFile{
		Until:  until.UTC().Format(time.RFC3339),
		Reason: reason,
		SetAt:  g.now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(g.Dir, "."+safeName(site)+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), g.cooldownPath(site))
}

func (g *Governor) ClearCooldown(site string) error {
	err := os.Remove(g.cooldownPath(site))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
