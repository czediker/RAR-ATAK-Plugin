package led

import (
	"errors"
	"testing"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/state"
)

var start = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

type snapOpt func(*state.Snapshot)

func snap(at time.Time, opts ...snapOpt) state.Snapshot {
	var s state.Snapshot
	s.Updated = at
	s.HaLow.State = state.HaLowConnected
	s.HaLow.Neighbors = 2
	s.Meshtastic.Connected = true
	for _, o := range opts {
		o(&s)
	}
	return s
}

func isolated(s *state.Snapshot) {
	s.HaLow.State, s.HaLow.Neighbors, s.Forwarding = state.HaLowIsolated, 0, true
}
func halowFault(s *state.Snapshot) { s.HaLow.Fault, s.HaLow.Error = true, "batctl: exit status 1" }
func meshFault(s *state.Snapshot) {
	s.Meshtastic.Fault, s.Meshtastic.Connected, s.Meshtastic.Error = true, false, "open: no such file"
}

func TestPatterns(t *testing.T) {
	now := start.Add(time.Second)
	cases := []struct {
		name        string
		opts        []snapOpt
		blue, green Pattern
	}{
		{"halow connected", nil, On, Off},
		{"fallover", []snapOpt{isolated}, Off, On},
		{"openMANET fault while connected", []snapOpt{halowFault}, Blink, Off},
		{"openMANET fault during fallover", []snapOpt{isolated, halowFault}, Blink, On},
		{"radio fault while connected", []snapOpt{meshFault}, On, Blink},
		{"radio fault during fallover", []snapOpt{isolated, meshFault}, Off, Blink},
		{"both faults", []snapOpt{isolated, halowFault, meshFault}, Blink, Blink},
	}
	for _, tc := range cases {
		e := NewEngine(DefaultConfig(), start)
		st := e.Evaluate(now, snap(now, tc.opts...), nil)
		if st.Blue != tc.blue || st.Green != tc.green || st.Red != Off {
			t.Errorf("%s: blue=%v green=%v red=%v, want blue=%v green=%v red=off", tc.name, st.Blue, st.Green, st.Red, tc.blue, tc.green)
		}
		if st.BlueWhy == "" || st.GreenWhy == "" {
			t.Errorf("%s: missing reasons %+v", tc.name, st)
		}
	}
}

func TestColorRendering(t *testing.T) {
	st := Status{Blue: Blink, Green: On}
	if c := st.Color(true); c != (Color{G: true, B: true}) {
		t.Errorf("phase on: %v", c)
	}
	if c := st.Color(false); c != (Color{G: true}) {
		t.Errorf("phase off: %v", c)
	}
	alt := Status{Blue: Blink, Green: BlinkAlt}
	if alt.Color(true) != (Color{B: true}) || alt.Color(false) != (Color{G: true}) {
		t.Error("alternate blink should swap colors")
	}
	if !alt.Blinking() || (Status{Blue: On}).Blinking() {
		t.Error("Blinking()")
	}
}

func TestStartupThenBridgeDown(t *testing.T) {
	e := NewEngine(DefaultConfig(), start)
	missing := errors.New("open /var/run/rar/state.json: no such file or directory")
	st := e.Evaluate(start.Add(time.Second), state.Snapshot{}, missing)
	if st.Blue != Blink || st.Green != BlinkAlt {
		t.Errorf("starting: %+v", st)
	}
	st = e.Evaluate(start.Add(91*time.Second), state.Snapshot{}, missing)
	if st.Blue != Blink || st.Green != Blink {
		t.Errorf("bridge down: %+v", st)
	}
}

func TestStaleStatusAfterRunning(t *testing.T) {
	e := NewEngine(DefaultConfig(), start)
	now := start.Add(time.Second)
	e.Evaluate(now, snap(now), nil)
	// Within the startup grace, but the bridge was seen running: this is
	// "bridge down", not "starting".
	st := e.Evaluate(now.Add(11*time.Second), snap(now), nil)
	if st.Blue != Blink || st.Green != Blink {
		t.Errorf("stale: %+v", st)
	}
}

func TestRedIsReserved(t *testing.T) {
	e := NewEngine(DefaultConfig(), start)
	now := start.Add(time.Second)
	for _, opts := range [][]snapOpt{nil, {isolated, halowFault, meshFault}} {
		if st := e.Evaluate(now, snap(now, opts...), nil); st.Red != Off || st.Color(true).R {
			t.Errorf("red must stay off: %+v", st)
		}
	}
	if st := e.Evaluate(now, state.Snapshot{}, errors.New("x")); st.Red != Off {
		t.Error("red must stay off when the bridge is down")
	}
	e.SetRed(Blink, "low battery")
	if st := e.Evaluate(now, snap(now), nil); st.Red != Blink || st.RedWhy != "low battery" {
		t.Errorf("red hook: %+v", st)
	}
}

func TestColorString(t *testing.T) {
	if (Color{}).String() != "off" || (Color{G: true, B: true}).String() != "GB" {
		t.Error("String()")
	}
}
