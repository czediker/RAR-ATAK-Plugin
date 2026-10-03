// Package led maps the bridge status to an RGB LED color.
//
//	Blue                 HaLow connected (traffic stays on HaLow)
//	Green                No HaLow neighbors: traffic is going over Meshtastic
//	Cyan                 HaLow connected and the plugin's "always send over
//	                     Meshtastic" toggle is on
//	<color>/Red blink    Meshtastic radio not connected
//	White flash          A packet was sent or received over Meshtastic
//	White slow blink     Starting up: waiting for the bridge service
//	Solid red            Bridge service not running (status file missing or
//	                     not updated)
package led

import (
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/state"
)

// Color is an on/off RGB value.
type Color struct{ R, G, B bool }

// Named colors.
var (
	Off   = Color{}
	Red   = Color{R: true}
	Green = Color{G: true}
	Blue  = Color{B: true}
	Cyan  = Color{G: true, B: true}
	White = Color{R: true, G: true, B: true}
)

func (c Color) String() string {
	switch c {
	case Off:
		return "off"
	case Red:
		return "red"
	case Green:
		return "green"
	case Blue:
		return "blue"
	case Cyan:
		return "cyan"
	case White:
		return "white"
	}
	s := ""
	for _, p := range []struct {
		on bool
		n  string
	}{{c.R, "R"}, {c.G, "G"}, {c.B, "B"}} {
		if p.on {
			s += p.n
		}
	}
	return s
}

// Mode names the condition the LED is showing (for logging).
type Mode string

// LED modes.
const (
	ModeStarting   Mode = "starting"
	ModeBridgeDown Mode = "bridge-down"
	ModeConnected  Mode = "halow-connected"
	ModeIsolated   Mode = "meshtastic-fallback"
	ModeForced     Mode = "meshtastic-forced"
	ModeRadioFault Mode = "radio-fault"
)

// Config tunes the indicator timing.
type Config struct {
	// Stale is how old the status file may get before the bridge is
	// considered down.
	Stale time.Duration
	// StartupGrace is how long to show "starting" before "bridge down".
	StartupGrace time.Duration
	// Flash is the duration of the TX/RX flash.
	Flash time.Duration
	// Blink is the period of blinking patterns.
	Blink time.Duration
}

// DefaultConfig returns the default timing.
func DefaultConfig() Config {
	return Config{Stale: 10 * time.Second, StartupGrace: 90 * time.Second, Flash: 150 * time.Millisecond, Blink: time.Second}
}

// Engine computes LED frames from successive status reads.
type Engine struct {
	cfg        Config
	start      time.Time
	everValid  bool
	havePrev   bool
	lastTx     uint64
	lastRx     uint64
	flashUntil time.Time
}

// NewEngine creates an engine; start is the service start time.
func NewEngine(cfg Config, start time.Time) *Engine {
	return &Engine{cfg: cfg, start: start}
}

// Frame returns the color to show at now given the latest status read.
func (e *Engine) Frame(now time.Time, snap state.Snapshot, readErr error) (Color, Mode) {
	if readErr != nil || now.Sub(snap.Updated) > e.cfg.Stale {
		if !e.everValid && now.Sub(e.start) < e.cfg.StartupGrace {
			return e.blink(now, White, Off), ModeStarting
		}
		e.havePrev = false
		return Red, ModeBridgeDown
	}
	e.everValid = true

	if e.havePrev && (snap.TxCount != e.lastTx || snap.RxCount != e.lastRx) {
		e.flashUntil = now.Add(e.cfg.Flash)
	}
	e.lastTx, e.lastRx, e.havePrev = snap.TxCount, snap.RxCount, true

	var base Color
	var mode Mode
	switch {
	case snap.HaLow.State == state.HaLowConnected && snap.Forced:
		base, mode = Cyan, ModeForced
	case snap.HaLow.State == state.HaLowConnected:
		base, mode = Blue, ModeConnected
	default:
		base, mode = Green, ModeIsolated
	}
	if now.Before(e.flashUntil) {
		return White, mode
	}
	if !snap.Meshtastic.Connected {
		return e.blink(now, base, Red), ModeRadioFault
	}
	return base, mode
}

// blink alternates between a and b, each for half the blink period.
func (e *Engine) blink(now time.Time, a, b Color) Color {
	half := e.cfg.Blink / 2
	if half <= 0 {
		return a
	}
	if (now.Sub(e.start)/half)%2 == 0 {
		return a
	}
	return b
}
