package halow

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const jsonOut = `[
    {"hard_ifindex": 3, "hard_ifname": "wlan0", "last_seen_msecs": 708, "neigh_address": "16:7b:3c:c2:bf:b8"},
    {"hard_ifindex": 3, "hard_ifname": "wlan0", "last_seen_msecs": 9872, "neigh_address": "ae:1b:bf:52:25:58"},
    {"hard_ifindex": 9, "hard_ifname": "battunnel0", "last_seen_msecs": 100, "neigh_address": "02:00:00:00:00:01"}
]`

// BATMAN_IV text layout.
const textIV = `[B.A.T.M.A.N. adv 2024.3-openwrt-6, MainIF/MAC: wlan0/2c:c6:82:8a:2b:ca (bat0/9a:c2:84:47:71:98 BATMAN_IV)]
IF             Neighbor              last-seen
          wlan0	  16:7b:3c:c2:bf:b8    0.740s
          wlan0	  ae:1b:bf:52:25:58   12.612s
`

// BATMAN_V text layout (with -H the header lines are absent).
const textV = `16:7b:3c:c2:bf:b8    0.320s (        1.0) [     wlan0]
ae:1b:bf:52:25:58    1.020s (       27.2) [battunnel0]
`

func TestParseNeighborsJSON(t *testing.T) {
	ns, err := ParseNeighborsJSON([]byte(jsonOut))
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 3 || ns[0].Iface != "wlan0" || ns[0].Address != "16:7b:3c:c2:bf:b8" || ns[0].LastSeen != 708*time.Millisecond {
		t.Fatalf("ns = %+v", ns)
	}
	if _, err := ParseNeighborsJSON([]byte("Error - no valid command")); err == nil {
		t.Error("expected error for non-JSON")
	}
	if ns, err := ParseNeighborsJSON([]byte("[]\n")); err != nil || len(ns) != 0 {
		t.Errorf("empty: %v %v", ns, err)
	}
}

func TestParseNeighborsText(t *testing.T) {
	iv := ParseNeighborsText([]byte(textIV))
	if len(iv) != 2 || iv[0].Iface != "wlan0" || iv[0].Address != "16:7b:3c:c2:bf:b8" || iv[0].LastSeen != 740*time.Millisecond || iv[1].LastSeen != 12612*time.Millisecond {
		t.Errorf("IV = %+v", iv)
	}
	v := ParseNeighborsText([]byte(textV))
	if len(v) != 2 || v[0].Iface != "wlan0" || v[0].Address != "16:7b:3c:c2:bf:b8" || v[1].Iface != "battunnel0" || v[0].LastSeen != 320*time.Millisecond {
		t.Errorf("V = %+v", v)
	}
	// bat-host names instead of MACs still parse.
	h := ParseNeighborsText([]byte("   wlan0	  manet02_wlan0    2.000s\n"))
	if len(h) != 1 || h[0].Address != "manet02_wlan0" {
		t.Errorf("hostname = %+v", h)
	}
}

func TestFilter(t *testing.T) {
	ns, _ := ParseNeighborsJSON([]byte(jsonOut))
	if got := Filter(ns, "wlan0", 5*time.Second); len(got) != 1 {
		t.Errorf("wlan0/5s = %+v", got)
	}
	if got := Filter(ns, Placeholder, 5*time.Second); len(got) != 2 {
		t.Errorf("placeholder should match any iface: %+v", got)
	}
	if got := Filter(ns, "", 0); len(got) != 3 {
		t.Errorf("no filters: %+v", got)
	}
}

func TestNeighborSourceFallsBackToText(t *testing.T) {
	var calls []string
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if args[len(args)-1] == "neighbors_json" {
			return nil, errors.New("exit status 1: Error - no valid command or debug table specified: neighbors_json")
		}
		return []byte(textIV), nil
	}
	s := &NeighborSource{Batctl: "/usr/sbin/batctl", HardIface: "wlan0", MaxAge: 5 * time.Second, Run: run}
	for i := 0; i < 2; i++ {
		ns, err := s.Neighbors(context.Background())
		if err != nil || len(ns) != 1 {
			t.Fatalf("Neighbors() = %v, %v", ns, err)
		}
	}
	want := []string{
		"/usr/sbin/batctl meshif bat0 neighbors_json",
		"/usr/sbin/batctl meshif bat0 neighbors -n -H",
		"/usr/sbin/batctl meshif bat0 neighbors -n -H", // JSON not retried
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestNeighborSourceErrorDoesNotStickToText(t *testing.T) {
	fail := true
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if fail {
			return nil, errors.New("bat0 missing")
		}
		return []byte(jsonOut), nil
	}
	s := &NeighborSource{Run: run}
	if _, err := s.Neighbors(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	fail = false
	if ns, err := s.Neighbors(context.Background()); err != nil || len(ns) != 3 || s.textOnly {
		t.Errorf("after recovery: %v %v textOnly=%v", ns, err, s.textOnly)
	}
}

func TestHysteresis(t *testing.T) {
	h := &Hysteresis{DownAfter: 15 * time.Second, UpAfter: 30 * time.Second}
	t0 := time.Unix(1000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

	if l, ch := h.Observe(at(0), true); l != LinkConnected || !ch {
		t.Fatalf("first observation: %v %v", l, ch)
	}
	// Neighbors vanish: stays connected until 15s of continuous absence.
	for _, s := range []int{5, 10, 15} {
		if l, _ := h.Observe(at(s), false); l != LinkConnected {
			t.Fatalf("t=%d: %v", s, l)
		}
	}
	// A blip of presence resets the timer.
	h.Observe(at(18), true)
	h.Observe(at(20), false)
	if l, _ := h.Observe(at(34), false); l != LinkConnected {
		t.Fatalf("timer should have reset: %v", l)
	}
	if l, ch := h.Observe(at(35), false); l != LinkIsolated || !ch {
		t.Fatalf("t=35: %v %v", l, ch)
	}
	// Coming back needs 30s of continuous presence.
	h.Observe(at(40), true)
	if l, _ := h.Observe(at(69), true); l != LinkIsolated {
		t.Fatalf("t=69: %v", l)
	}
	if l, ch := h.Observe(at(70), true); l != LinkConnected || !ch {
		t.Fatalf("t=70: %v %v", l, ch)
	}
	if LinkUnknown.String() != "unknown" || LinkConnected.String() != "connected" || LinkIsolated.String() != "isolated" {
		t.Error("String()")
	}
}

type fakeLister struct {
	results chan []Neighbor
}

func (f *fakeLister) Neighbors(ctx context.Context) ([]Neighbor, error) {
	select {
	case ns := <-f.results:
		if ns == nil {
			return nil, errors.New("batctl failed")
		}
		return ns, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestMonitorTreatsErrorsAsNoNeighbors(t *testing.T) {
	f := &fakeLister{results: make(chan []Neighbor, 4)}
	f.results <- []Neighbor{{Iface: "wlan0"}}
	f.results <- nil // error
	m := &Monitor{
		Source: f, Poll: time.Millisecond,
		Hyst: &Hysteresis{DownAfter: 0, UpAfter: time.Hour},
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan Report)
	go m.Run(ctx, out)
	if r := <-out; r.Link != LinkConnected || r.Neighbors != 1 {
		t.Fatalf("first report %+v", r)
	}
	if r := <-out; r.Link != LinkIsolated || r.Err == nil {
		t.Fatalf("second report %+v", r)
	}
}
