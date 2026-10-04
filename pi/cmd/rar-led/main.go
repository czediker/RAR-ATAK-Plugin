// Command rar-led shows the radio status on the RGB LED. It only reads the
// status file written by rar-bridge, so either service can restart
// independently.
//
// The status file is read once per -interval (default 1 s; rar-bridge
// updates it every second). A separate half-period clock (500 ms) only
// toggles blinking colors; GPIO lines are written only when the color
// actually changes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/led"
	"github.com/czediker/rar-atak-plugin/pi/internal/state"
)

var version = "dev"

func main() {
	def := led.DefaultConfig()
	var (
		statePath = flag.String("state-file", state.DefaultPath, "status file written by rar-bridge")
		chip      = flag.String("gpiochip", "gpiochip0", "GPIO character device")
		red       = flag.Int("red", 16, "GPIO (BCM) number of the red LED (reserved; held off)")
		green     = flag.Int("green", 20, "GPIO (BCM) number of the green LED")
		blue      = flag.Int("blue", 21, "GPIO (BCM) number of the blue LED")
		activeLow = flag.Bool("active-low", false, "pin low turns a color on (common-anode LED); default is common cathode, pin high = on")
		stale     = flag.Duration("stale", def.Stale, "status older than this means the bridge is down")
		grace     = flag.Duration("startup-grace", def.StartupGrace, "show 'starting' this long before 'bridge down'")
		interval  = flag.Duration("interval", time.Second, "how often to read the status file")
		blink     = flag.Duration("blink", time.Second, "blink period (on + off)")
		redFile   = flag.String("red-file", "", "hook for the reserved red LED: a file containing on, off or blink (empty = red unused)")
		debug     = flag.Bool("debug", false, "log every LED change with its reason, and status file problems")
		dryRun    = flag.Bool("dry-run", false, "log colors instead of driving GPIO")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", "rar-led")

	var drv led.Driver
	if *dryRun {
		drv = led.LogDriver{Log: log}
	} else {
		g, err := led.OpenGPIO(*chip, *red, *green, *blue, *activeLow)
		if err != nil {
			log.Error("cannot open LED GPIOs", "err", err)
			os.Exit(1)
		}
		drv = g
	}
	defer drv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := def
	cfg.Stale = *stale
	cfg.StartupGrace = *grace
	eng := led.NewEngine(cfg, time.Now())
	log.Info("starting rar-led", "version", version, "state_file", *statePath,
		"pins", fmt.Sprintf("red=%d(reserved) green=%d blue=%d", *red, *green, *blue),
		"polarity", map[bool]string{false: "active-high (common cathode)", true: "active-low (common anode)"}[*activeLow],
		"interval", *interval, "debug", *debug)

	r := &runner{log: log, drv: drv, eng: eng, statePath: *statePath, redFile: *redFile}
	r.update(time.Now())

	readT := time.NewTicker(*interval)
	defer readT.Stop()
	blinkT := time.NewTicker(max(*blink/2, 50*time.Millisecond))
	defer blinkT.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("stopping rar-led; LED off")
			return
		case <-readT.C:
			r.update(time.Now())
		case <-blinkT.C:
			if r.status.Blinking() {
				r.phase = !r.phase
				r.apply()
			}
		}
	}
}

type runner struct {
	log       *slog.Logger
	drv       led.Driver
	eng       *led.Engine
	statePath string
	redFile   string

	status   led.Status
	started  bool
	phase    bool
	current  led.Color
	written  bool
	readErr  string
	redValue string
}

// update reads the status file (and red hook) and commits any change.
func (r *runner) update(now time.Time) {
	r.readRedHook()
	snap, err := state.Read(r.statePath)
	if msg := errString(err); msg != r.readErr {
		if err != nil {
			r.log.Debug("cannot read status file", "path", r.statePath, "err", err)
		} else {
			r.log.Debug("status file readable again", "path", r.statePath)
		}
		r.readErr = msg
	}
	next := r.eng.Evaluate(now, snap, err)
	if !r.started || next.Blue != r.status.Blue || next.BlueWhy != r.status.BlueWhy {
		r.logChange("blue", r.status.Blue, next.Blue, next.BlueWhy)
	}
	if !r.started || next.Green != r.status.Green || next.GreenWhy != r.status.GreenWhy {
		r.logChange("green", r.status.Green, next.Green, next.GreenWhy)
	}
	if !r.started || next.Red != r.status.Red || next.RedWhy != r.status.RedWhy {
		r.logChange("red", r.status.Red, next.Red, next.RedWhy)
	}
	if !r.status.Blinking() && next.Blinking() {
		r.phase = true // start a blink with the color lit
	}
	r.status, r.started = next, true
	r.apply()
}

func (r *runner) logChange(color string, from, to led.Pattern, why string) {
	if r.started && from == to {
		// Same pattern, new reason (e.g. a different error): debug only.
		r.log.Debug("LED reason changed", "color", color, "pattern", to, "reason", why)
		return
	}
	if !r.started {
		r.log.Info("LED change committed", "color", color, "to", to, "reason", why)
		return
	}
	r.log.Info("LED change committed", "color", color, "from", from, "to", to, "reason", why)
}

// apply writes the pins when the rendered color differs from what is lit.
func (r *runner) apply() {
	c := r.status.Color(r.phase)
	if r.written && c == r.current {
		return
	}
	if err := r.drv.Set(c); err != nil {
		r.log.Warn("cannot set LED", "err", err)
		return
	}
	r.current, r.written = c, true
}

// readRedHook applies the reserved red LED hook file, if configured.
func (r *runner) readRedHook() {
	if r.redFile == "" {
		return
	}
	b, err := os.ReadFile(r.redFile)
	v := strings.ToLower(strings.TrimSpace(string(b)))
	if err != nil {
		v = "off"
	}
	if v == r.redValue {
		return
	}
	r.redValue = v
	switch v {
	case "on", "1":
		r.eng.SetRed(led.On, "red hook ("+r.redFile+")")
	case "blink":
		r.eng.SetRed(led.Blink, "red hook ("+r.redFile+")")
	default:
		r.eng.SetRed(led.Off, "red hook off ("+r.redFile+")")
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
