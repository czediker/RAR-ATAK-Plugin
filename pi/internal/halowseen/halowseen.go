// Package halowseen records which ATAK users are currently reachable over
// the HaLow mesh by passively listening to ATAK's SA and GeoChat multicast
// traffic.
//
// The bridge uses it to drop Meshtastic copies of traffic the local EUD
// already received over HaLow (which happens when a sender uses the
// "always send over Meshtastic" toggle). It is also the basis for future
// isolated-radio detection: a UID heard over Meshtastic but absent here is
// a radio that HaLow cannot reach.
package halowseen

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/cot"
)

// Default ATAK multicast groups.
const (
	SAGroup   = "239.2.3.1:6969"
	ChatGroup = "224.10.10.1:17012"
)

// Tracker remembers when each ATAK UID was last heard over HaLow. It is
// safe for concurrent use.
type Tracker struct {
	window time.Duration
	// Log, if set, receives a debug line whenever a user starts being heard
	// over HaLow.
	Log *slog.Logger

	mu        sync.Mutex
	seen      map[string]time.Time
	listenErr map[string]string // multicast group → last error
}

// NewTracker creates a tracker; UIDs not heard for window are forgotten.
func NewTracker(window time.Duration) *Tracker {
	return &Tracker{window: window, seen: map[string]time.Time{}, listenErr: map[string]string{}}
}

// Mark records uid as heard at now.
func (t *Tracker) Mark(uid string, now time.Time) {
	if uid == "" {
		return
	}
	t.mu.Lock()
	last, ok := t.seen[uid]
	t.seen[uid] = now
	t.mu.Unlock()
	if t.Log != nil && (!ok || now.Sub(last) > t.window) {
		t.Log.Debug("ATAK user heard over HaLow (Meshtastic copies from this user will be de-duplicated)", "uid", uid)
	}
}

// LastHeard returns when uid was last heard over HaLow.
func (t *Tracker) LastHeard(uid string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, ok := t.seen[uid]
	return last, ok
}

// ListenerError describes multicast listeners that are currently failing
// (empty when all are healthy).
func (t *Tracker) ListenerError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var parts []string
	for g, e := range t.listenErr {
		parts = append(parts, g+": "+e)
	}
	slices.Sort(parts)
	return strings.Join(parts, "; ")
}

func (t *Tracker) setListenerError(group string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err == nil {
		delete(t.listenErr, group)
	} else {
		t.listenErr[group] = err.Error()
	}
}

// Observe parses a CoT datagram and marks its sender.
func (t *Tracker) Observe(data []byte, now time.Time) {
	ev, err := cot.Parse(data)
	if err != nil {
		return
	}
	switch {
	case ev.IsChat():
		t.Mark(ev.Chat.SenderUID, now)
	case ev.IsPLI():
		t.Mark(ev.UID, now)
	}
}

// Recently reports whether uid was heard within the window.
func (t *Tracker) Recently(uid string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, ok := t.seen[uid]
	return ok && now.Sub(last) <= t.window
}

// UIDs returns the UIDs heard within the window.
func (t *Tracker) UIDs(now time.Time) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for uid, last := range t.seen {
		if now.Sub(last) <= t.window {
			out = append(out, uid)
		} else {
			delete(t.seen, uid)
		}
	}
	slices.Sort(out)
	return out
}

// Listen joins the multicast group (host:port) on iface (empty = system
// default) and feeds every datagram to the tracker until ctx is cancelled.
// It keeps retrying if the interface is not up yet.
func Listen(ctx context.Context, iface, group string, t *Tracker, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "halowseen", "group", group)
	backoff := time.Second
	for ctx.Err() == nil {
		err := listenOnce(ctx, iface, group, t, log)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("listener stopped")
		}
		t.setListenerError(group, err)
		log.Warn("multicast listener stopped; retrying", "iface", iface, "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func listenOnce(ctx context.Context, iface, group string, t *Tracker, log *slog.Logger) error {
	gaddr, err := net.ResolveUDPAddr("udp4", group)
	if err != nil {
		return err
	}
	var ifi *net.Interface
	if iface != "" {
		if ifi, err = net.InterfaceByName(iface); err != nil {
			return err
		}
	}
	conn, err := net.ListenMulticastUDP("udp4", ifi, gaddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	t.setListenerError(group, nil)
	log.Info("listening for ATAK multicast", "iface", iface)

	buf := make([]byte, 64*1024)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) && ctx.Err() != nil {
				return nil
			}
			return err
		}
		t.Observe(buf[:n], time.Now())
	}
}
