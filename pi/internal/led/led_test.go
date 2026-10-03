package led

import (
	"errors"
	"testing"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/state"
)

var start = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func snap(at time.Time, halow string, radio, forced bool, tx, rx uint64) state.Snapshot {
	var s state.Snapshot
	s.Updated = at
	s.HaLow.State = halow
	s.Meshtastic.Connected = radio
	s.Forced = forced
	s.TxCount, s.RxCount = tx, rx
	return s
}

func TestBaseColors(t *testing.T) {
	cases := []struct {
		name   string
		halow  string
		forced bool
		want   Color
		mode   Mode
	}{
		{"connected", state.HaLowConnected, false, Blue, ModeConnected},
		{"isolated", state.HaLowIsolated, false, Green, ModeIsolated},
		{"unknown", state.HaLowUnknown, false, Green, ModeIsolated},
		{"forced", state.HaLowConnected, true, Cyan, ModeForced},
		{"forced-but-isolated", state.HaLowIsolated, true, Green, ModeIsolated},
	}
	for _, tc := range cases {
		e := NewEngine(DefaultConfig(), start)
		now := start.Add(time.Second)
		c, m := e.Frame(now, snap(now, tc.halow, true, tc.forced, 0, 0), nil)
		if c != tc.want || m != tc.mode {
			t.Errorf("%s: got %v/%v want %v/%v", tc.name, c, m, tc.want, tc.mode)
		}
	}
}

func TestStartupThenBridgeDown(t *testing.T) {
	e := NewEngine(DefaultConfig(), start)
	missing := errors.New("no file")
	c0, m := e.Frame(start, state.Snapshot{}, missing)
	c1, _ := e.Frame(start.Add(600*time.Millisecond), state.Snapshot{}, missing)
	if m != ModeStarting || c0 != White || c1 != Off {
		t.Errorf("starting blink: %v %v %v", c0, c1, m)
	}
	if c, m := e.Frame(start.Add(91*time.Second), state.Snapshot{}, missing); c != Red || m != ModeBridgeDown {
		t.Errorf("after grace: %v %v", c, m)
	}
}

func TestStaleStatusIsBridgeDown(t *testing.T) {
	e := NewEngine(DefaultConfig(), start)
	now := start.Add(time.Second)
	e.Frame(now, snap(now, state.HaLowConnected, true, false, 0, 0), nil)
	later := now.Add(11 * time.Second)
	if c, m := e.Frame(later, snap(now, state.HaLowConnected, true, false, 0, 0), nil); c != Red || m != ModeBridgeDown {
		t.Errorf("stale: %v %v", c, m)
	}
}

func TestRadioFaultBlinksRed(t *testing.T) {
	e := NewEngine(DefaultConfig(), start)
	a := start
	b := start.Add(600 * time.Millisecond)
	c1, m := e.Frame(a, snap(a, state.HaLowConnected, false, false, 0, 0), nil)
	c2, _ := e.Frame(b, snap(b, state.HaLowConnected, false, false, 0, 0), nil)
	if m != ModeRadioFault || c1 != Blue || c2 != Red {
		t.Errorf("fault blink: %v %v %v", c1, c2, m)
	}
}

func TestFlashOnTraffic(t *testing.T) {
	e := NewEngine(DefaultConfig(), start)
	now := start.Add(time.Second)
	// First read never flashes.
	if c, _ := e.Frame(now, snap(now, state.HaLowIsolated, true, false, 5, 2), nil); c != Green {
		t.Fatalf("first frame %v", c)
	}
	now = now.Add(100 * time.Millisecond)
	if c, _ := e.Frame(now, snap(now, state.HaLowIsolated, true, false, 6, 2), nil); c != White {
		t.Errorf("tx should flash, got %v", c)
	}
	now = now.Add(100 * time.Millisecond)
	if c, _ := e.Frame(now, snap(now, state.HaLowIsolated, true, false, 6, 2), nil); c != White {
		t.Errorf("flash should last 150ms, got %v", c)
	}
	now = now.Add(100 * time.Millisecond)
	if c, _ := e.Frame(now, snap(now, state.HaLowIsolated, true, false, 6, 2), nil); c != Green {
		t.Errorf("flash should end, got %v", c)
	}
	if c, _ := e.Frame(now, snap(now, state.HaLowIsolated, true, false, 6, 3), nil); c != White {
		t.Errorf("rx should flash, got %v", c)
	}
}

func TestColorString(t *testing.T) {
	if Cyan.String() != "cyan" || (Color{R: true, B: true}).String() != "RB" {
		t.Error("String()")
	}
}
