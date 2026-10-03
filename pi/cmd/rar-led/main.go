// Command rar-led shows the radio status on an RGB LED. It only reads the
// status file written by rar-bridge, so either service can restart
// independently.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
		red       = flag.Int("red", 16, "GPIO (BCM) number of the red cathode")
		green     = flag.Int("green", 20, "GPIO (BCM) number of the green cathode")
		blue      = flag.Int("blue", 21, "GPIO (BCM) number of the blue cathode")
		activeLow = flag.Bool("active-low", true, "pin low turns the LED on (common anode wired directly to the pins)")
		stale     = flag.Duration("stale", def.Stale, "status older than this means the bridge is down")
		grace     = flag.Duration("startup-grace", def.StartupGrace, "show 'starting' this long before 'bridge down'")
		interval  = flag.Duration("interval", 50*time.Millisecond, "refresh interval")
		dryRun    = flag.Bool("dry-run", false, "log colors instead of driving GPIO")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

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
	log.Info("starting rar-led", "version", version, "state_file", *statePath, "pins", fmt.Sprintf("R%d G%d B%d", *red, *green, *blue), "active_low", *activeLow)

	t := time.NewTicker(*interval)
	defer t.Stop()
	current := led.Color{R: true, G: true, B: true}
	var mode led.Mode
	first := true
	for {
		snap, err := state.Read(*statePath)
		c, m := eng.Frame(time.Now(), snap, err)
		if m != mode {
			log.Info("status", "mode", m)
			mode = m
		}
		if c != current || first {
			if err := drv.Set(c); err != nil {
				log.Warn("cannot set LED", "err", err)
			}
			current, first = c, false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
