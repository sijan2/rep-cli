package governor

import (
	"context"
	"sync"
	"time"
)

// Pacer spaces successive Wait returns at least min apart. Pacing is a load
// limit, not camouflage, so there is no jitter: slots are reserved under the
// mutex and callers sleep until their slot, which also serializes concurrent
// callers into one chain.
type Pacer struct {
	min   time.Duration
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
	mu    sync.Mutex
	next  time.Time
}

func (g *Governor) Pacer(min time.Duration) *Pacer {
	if min < 0 {
		min = 0
	}
	return &Pacer{min: min, now: g.now, sleep: sleepFor}
}

// Wait returns at the next free slot or when ctx ends. A cancelled wait leaves
// its slot unused, so later callers are only ever slower, never faster. The
// clock is re-read after every sleep because a relative sleep started a little
// after the clock was sampled can end before the slot; returning early would
// let two waiters land closer than the floor.
func (p *Pacer) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	now := p.now()
	slot := p.next
	if slot.Before(now) {
		slot = now
	}
	p.next = slot.Add(p.min)
	p.mu.Unlock()
	for {
		remaining := slot.Sub(p.now())
		if remaining <= 0 {
			return nil
		}
		if err := p.sleep(ctx, remaining); err != nil {
			return err
		}
	}
}

func sleepFor(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
