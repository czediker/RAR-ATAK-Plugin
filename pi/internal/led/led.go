// Package led maps the bridge status to the RGB status LED. Each color has
// its own job:
//
//	Blue  solid      HaLow mesh has neighbors; ATAK traffic stays on HaLow
//	Blue  blinking   openMANET problem: the neighbor query (batctl) or the
//	                 ATAK multicast listener on the mesh bridge is failing
//	Green solid      Fallover: no HaLow neighbors, traffic goes over Meshtastic
//	Green blinking   Meshtastic radio problem: not connected, or a packet
//	                 failed, was rejected or was not confirmed
//	Red              Reserved (planned: low battery). Off unless driven
//	                 through the red hook (Engine.SetRed / rar-led -red-file).
//
// A fault blinks its own color whatever the fallover state is. Two patterns
// cover the bridge itself: blue and green alternating while waiting for
// rar-bridge at startup, and blue and green blinking together when its
// status file is missing or no longer updated.
package led

import (
	"fmt"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/state"
)

// Color is an on/off RGB value as written to the pins.
type Color struct{ R, G, B bool }

func (c Color) String() string {
	s := ""
	for _, p := range []struct {
		on bool
		n  string
	}{{c.R, "R"}, {c.G, "G"}, {c.B, "B"}} {
		if p.on {
			s += p.n
		}
	}
	if s == "" {
		return "off"
	}
	return s
}

// Pattern is what one color of the LED is doing.
type Pattern int

// Patterns.
const (
	Off Pattern = iota
	On
	// Blink toggles every half blink period.
	Blink
	// BlinkAlt blinks in opposite phase to Blink (used to alternate colors).
	BlinkAlt
)

func (p Pattern) String() string {
	switch p {
	case On:
		return "solid"
	case Blink:
		return "blinking"
	case BlinkAlt:
		return "blinking (alternate)"
	default:
		return "off"
	}
}

func (p Pattern) lit(phase bool) bool {
	switch p {
	case On:
		return true
	case Blink:
		return phase
	case BlinkAlt:
		return !phase
	default:
		return false
	}
}

// Status is the pattern of each color plus why it was chosen.
type Status struct {
	Red, Green, Blue Pattern
	GreenWhy         string
	BlueWhy          string
	RedWhy           string
}

// Color renders the status for one blink phase.
func (s Status) Color(phase bool) Color {
	return Color{R: s.Red.lit(phase), G: s.Green.lit(phase), B: s.Blue.lit(phase)}
}

// Blinking reports whether any color blinks (so the blink clock matters).
func (s Status) Blinking() bool {
	for _, p := range []Pattern{s.Red, s.Green, s.Blue} {
		if p == Blink || p == BlinkAlt {
			return true
		}
	}
	return false
}

// Config tunes the indicator.
type Config struct {
	// Stale is how old the status file may get before the bridge is
	// considered down.
	Stale time.Duration
	// StartupGrace is how long to show "starting" before "bridge down".
	StartupGrace time.Duration
}

// DefaultConfig returns the default timing.
func DefaultConfig() Config {
	return Config{Stale: 10 * time.Second, StartupGrace: 90 * time.Second}
}

// Engine turns status file reads into LED patterns.
type Engine struct {
	cfg       Config
	start     time.Time
	everValid bool
	red       Pattern
	redWhy    string
}

// NewEngine creates an engine; start is the service start time.
func NewEngine(cfg Config, start time.Time) *Engine {
	return &Engine{cfg: cfg, start: start, redWhy: "reserved (not used)"}
}

// SetRed is the hook for the reserved red LED (e.g. a future low-battery
// monitor). Nothing in rar-bridge's status drives red.
func (e *Engine) SetRed(p Pattern, why string) {
	e.red, e.redWhy = p, why
}

// Evaluate decides the patterns from the latest status file read.
func (e *Engine) Evaluate(now time.Time, snap state.Snapshot, readErr error) Status {
	st := Status{Red: e.red, RedWhy: e.redWhy}

	problem := ""
	switch {
	case readErr != nil:
		problem = readErr.Error()
	case now.Sub(snap.Updated) > e.cfg.Stale:
		problem = fmt.Sprintf("status file not updated for %s", now.Sub(snap.Updated).Round(time.Second))
	}
	if problem != "" {
		if !e.everValid && now.Sub(e.start) < e.cfg.StartupGrace {
			st.Blue, st.Green = Blink, BlinkAlt
			st.BlueWhy = "starting: waiting for rar-bridge (" + problem + ")"
			st.GreenWhy = st.BlueWhy
			return st
		}
		st.Blue, st.Green = Blink, Blink
		st.BlueWhy = "rar-bridge not running? (" + problem + ")"
		st.GreenWhy = st.BlueWhy
		return st
	}
	e.everValid = true

	switch {
	case snap.HaLow.Fault:
		st.Blue, st.BlueWhy = Blink, "openMANET problem: "+snap.HaLow.Error
	case snap.HaLow.State == state.HaLowConnected:
		st.Blue, st.BlueWhy = On, fmt.Sprintf("HaLow connected (%d neighbors)", snap.HaLow.Neighbors)
	default:
		st.Blue, st.BlueWhy = Off, "HaLow "+snap.HaLow.State
	}

	switch {
	case snap.Meshtastic.Fault:
		st.Green, st.GreenWhy = Blink, "Meshtastic radio problem: "+snap.Meshtastic.Error
	case snap.Forwarding:
		st.Green, st.GreenWhy = On, "fallover: traffic going over Meshtastic"
	default:
		st.Green, st.GreenWhy = Off, "Meshtastic standby (HaLow in use)"
	}
	return st
}
