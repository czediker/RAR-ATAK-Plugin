package halow

import (
	"context"
	"log/slog"
	"time"
)

// Link is the HaLow mesh state as seen by the failover logic.
type Link int

const (
	// LinkUnknown is the state before the first neighbor check.
	LinkUnknown Link = iota
	// LinkConnected means at least one HaLow neighbor is reachable.
	LinkConnected
	// LinkIsolated means no HaLow neighbors: traffic falls back to
	// Meshtastic.
	LinkIsolated
)

func (l Link) String() string {
	switch l {
	case LinkConnected:
		return "connected"
	case LinkIsolated:
		return "isolated"
	default:
		return "unknown"
	}
}

// Hysteresis debounces neighbor observations so the radio does not flap
// between HaLow and Meshtastic at the edge of range.
type Hysteresis struct {
	// DownAfter is how long neighbors must be absent before going isolated.
	DownAfter time.Duration
	// UpAfter is how long neighbors must be present before going connected.
	UpAfter time.Duration

	state   Link
	pending time.Time // start of the run of observations contradicting state
}

// State returns the current debounced state.
func (h *Hysteresis) State() Link { return h.state }

// Observe feeds one observation and returns the (possibly new) state and
// whether it changed. The first observation sets the state immediately.
func (h *Hysteresis) Observe(now time.Time, haveNeighbors bool) (Link, bool) {
	want := LinkIsolated
	if haveNeighbors {
		want = LinkConnected
	}
	switch {
	case h.state == LinkUnknown:
		h.state, h.pending = want, time.Time{}
		return h.state, true
	case want == h.state:
		h.pending = time.Time{}
		return h.state, false
	}
	if h.pending.IsZero() {
		h.pending = now
	}
	hold := h.DownAfter
	if want == LinkConnected {
		hold = h.UpAfter
	}
	if now.Sub(h.pending) >= hold {
		h.state, h.pending = want, time.Time{}
		return h.state, true
	}
	return h.state, false
}

// Report is published after every neighbor check.
type Report struct {
	At        time.Time
	Link      Link
	Neighbors int
	Err       error
}

// NeighborLister is satisfied by *NeighborSource.
type NeighborLister interface {
	Neighbors(ctx context.Context) ([]Neighbor, error)
}

// Monitor polls the neighbor table and publishes debounced link state.
type Monitor struct {
	Source NeighborLister
	Poll   time.Duration
	Hyst   *Hysteresis
	Log    *slog.Logger
	Now    func() time.Time
}

// Run polls until ctx is cancelled, sending a Report after every check. A
// failed check counts as "no neighbors" so failures fail over to
// Meshtastic rather than silently dropping traffic.
func (m *Monitor) Run(ctx context.Context, out chan<- Report) {
	now := m.Now
	if now == nil {
		now = time.Now
	}
	log := m.Log
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(m.Poll)
	defer t.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, max(m.Poll, time.Second))
		ns, err := m.Source.Neighbors(cctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		at := now()
		link, changed := m.Hyst.Observe(at, err == nil && len(ns) > 0)
		if err != nil {
			log.Warn("neighbor check failed", "err", err)
		}
		if changed {
			log.Info("HaLow link state", "state", link, "neighbors", len(ns))
		}
		select {
		case out <- Report{At: at, Link: link, Neighbors: len(ns), Err: err}:
		case <-ctx.Done():
			return
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}
