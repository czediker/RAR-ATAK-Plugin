// Command rar-bridge forwards ATAK traffic between the EUD and the
// Meshtastic radio depending on HaLow mesh connectivity.
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

	"github.com/czediker/rar-atak-plugin/pi/internal/bridge"
	"github.com/czediker/rar-atak-plugin/pi/internal/eud"
	"github.com/czediker/rar-atak-plugin/pi/internal/halow"
	"github.com/czediker/rar-atak-plugin/pi/internal/halowseen"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshtastic"
	"github.com/czediker/rar-atak-plugin/pi/internal/state"
	"github.com/czediker/rar-atak-plugin/pi/internal/uartcheck"
)

var version = "dev"

func main() {
	def := bridge.DefaultConfig()
	var (
		// Meshtastic radio.
		serialDev = flag.String("serial", "/dev/ttyAMA2", "serial device connected to the RAK4631 (uart2 on pins 27/28)")
		baud      = flag.Int("baud", 115200, "serial baud rate (must match the Meshtastic Serial module)")
		channel   = flag.Uint("channel", 0, "Meshtastic channel index to transmit on")
		hopLimit  = flag.Uint("hop-limit", 0, "hop limit for transmitted packets (0 = device setting)")

		// HaLow neighbor detection.
		batctl      = flag.String("batctl", "batctl", "path to batctl")
		meshIface   = flag.String("mesh-iface", "bat0", "batman-adv mesh interface")
		halowIface  = flag.String("halow-iface", halow.Placeholder, "HaLow hard interface whose neighbors count (placeholder/empty = any)")
		poll        = flag.Duration("poll", 5*time.Second, "neighbor check interval")
		neighborAge = flag.Duration("neighbor-max-age", 5*time.Second, "ignore neighbors not heard for this long")
		downAfter   = flag.Duration("down-after", 15*time.Second, "switch to Meshtastic after this long with no neighbors")
		upAfter     = flag.Duration("up-after", 30*time.Second, "switch back to HaLow after neighbors are present this long")

		// Plugin side.
		listenAuto   = flag.String("listen-auto", ":6700", "UDP address for plugin traffic forwarded only when isolated")
		listenAlways = flag.String("listen-always", ":6701", "UDP address for plugin traffic always forwarded")
		eudPort      = flag.Uint("eud-port", 4242, "UDP port on the EUD that received traffic is sent to (ATAK's default input)")
		eudTTL       = flag.Duration("eud-ttl", 15*time.Minute, "forget an EUD this long after its last packet")
		leaseFile    = flag.String("lease-file", eud.DefaultLeaseFile, "dnsmasq lease file; its clients also receive traffic (empty = off)")

		// HaLow duplicate suppression.
		mcastIface   = flag.String("mcast-iface", "br-ahwlan", "interface to listen for ATAK multicast on (empty = default)")
		saGroup      = flag.String("sa-group", halowseen.SAGroup, "ATAK SA multicast group")
		chatGroup    = flag.String("chat-group", halowseen.ChatGroup, "ATAK GeoChat multicast group")
		suppressDups = flag.Bool("suppress-halow-dupes", def.SuppressHaLowDupes, "drop Meshtastic traffic from users currently heard over HaLow")
		seenWindow   = flag.Duration("halow-seen-window", 60*time.Second, "how long a user counts as heard over HaLow")

		// Rate limiting and timing.
		pliInterval    = flag.Duration("pli-interval", def.PLIInterval, "minimum spacing of position reports per user")
		pliMinInterval = flag.Duration("pli-min-interval", def.PLIMinInterval, "minimum spacing when the user is moving")
		pliMove        = flag.Float64("pli-move-meters", def.PLIMoveMeters, "movement that allows the shorter interval")
		txGap          = flag.Duration("tx-gap", def.TxGap, "minimum time between Meshtastic transmissions")
		chatMaxAge     = flag.Duration("chat-max-age", def.ChatMaxAge, "drop chats that cannot be sent within this time")
		forcedWindow   = flag.Duration("forced-window", def.ForcedWindow, "how long 'always send' mode is reported after its last packet")
		pliStale       = flag.Duration("pli-stale", def.PLIStale, "stale time of received position reports")

		statePath = flag.String("state-file", state.DefaultPath, "status file read by rar-led")
		logLevel  = flag.String("log-level", "info", "debug, info, warn or error")
		debug     = flag.Bool("debug", false, "log every message, decision, de-duplication and state change (same as -log-level debug)")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}

	level := parseLevel(*logLevel)
	if *debug {
		level = slog.LevelDebug
	}
	// stdout: procd logs it as daemon.info (stderr would show as daemon.err).
	// Every line carries service=rar-bridge.
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", "rar-bridge")
	slog.SetDefault(log)
	log.Info("starting rar-bridge", "version", version, "serial", *serialDev, "baud", *baud, "halow_iface", *halowIface, "debug", level == slog.LevelDebug)
	if *halowIface == halow.Placeholder || *halowIface == "" {
		log.Warn("HaLow interface not configured; counting batman-adv neighbors on every interface")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The radio confirms every packet handed to it with a QueueStatus; pass
	// those to the bridge so it can report success or failure.
	acks := make(chan *meshpb.QueueStatus, 64)
	radio := meshtastic.New(meshtastic.Config{
		HopLimit: uint32(*hopLimit),
		Channel:  uint32(*channel),
		Logger:   log,
		OnFromRadio: func(m *meshpb.FromRadio) {
			if qs := m.GetQueueStatus(); qs != nil {
				select {
				case acks <- qs:
				default:
				}
			}
		},
	}, meshtastic.SerialOpener(*serialDev, *baud))
	radioDone := make(chan struct{})
	go func() {
		radio.Run(ctx)
		close(radioDone)
	}()
	go watchPort(ctx, *serialDev, radio, log)

	reports := make(chan halow.Report, 1)
	mon := &halow.Monitor{
		Source: &halow.NeighborSource{Batctl: *batctl, MeshIface: *meshIface, HardIface: *halowIface, MaxAge: *neighborAge},
		Poll:   *poll,
		Hyst:   &halow.Hysteresis{DownAfter: *downAfter, UpAfter: *upAfter},
		Log:    log.With("component", "halow"),
	}
	go mon.Run(ctx, reports)

	var seen *halowseen.Tracker
	if *suppressDups {
		seen = halowseen.NewTracker(*seenWindow)
		seen.Log = log.With("component", "halowseen")
		for _, g := range []string{*saGroup, *chatGroup} {
			if g != "" {
				go halowseen.Listen(ctx, *mcastIface, g, seen, log)
			}
		}
	}

	inbound := make(chan bridge.Datagram, 64)
	for _, l := range []struct {
		addr string
		port bridge.Port
	}{{*listenAuto, bridge.PortAuto}, {*listenAlways, bridge.PortAlways}} {
		if err := bridge.ListenUDP(ctx, l.addr, l.port, inbound, log); err != nil {
			log.Error("cannot listen for plugin traffic", "addr", l.addr, "err", err)
			os.Exit(1)
		}
	}

	deliver, err := bridge.NewUDPDeliverer(uint16(*eudPort))
	if err != nil {
		log.Error("cannot open delivery socket", "err", err)
		os.Exit(1)
	}
	defer deliver.Close()

	cfg := def
	cfg.PLIInterval = *pliInterval
	cfg.PLIMinInterval = *pliMinInterval
	cfg.PLIMoveMeters = *pliMove
	cfg.TxGap = *txGap
	cfg.ChatMaxAge = *chatMaxAge
	cfg.ForcedWindow = *forcedWindow
	cfg.PLIStale = *pliStale
	cfg.SuppressHaLowDupes = *suppressDups

	b := bridge.New(cfg, radio, deliver, eud.NewRegistry(*eudTTL), seen, log)
	b.Loop(ctx, bridge.LoopConfig{
		Inbound:   inbound,
		Radio:     radio.Packets(),
		Acks:      acks,
		Reports:   reports,
		StatePath: *statePath,
		LeaseFile: *leaseFile,
	})
	// Let the client tell the radio it is leaving, so the radio ends the
	// serial session (and turns Bluetooth back on) straight away.
	select {
	case <-radioDone:
	case <-time.After(2 * time.Second):
	}
	log.Info("rar-bridge stopped")
}

// Compile-time check that the client satisfies the bridge's interface.
var _ bridge.Radio = (*meshtastic.Client)(nil)

// watchPort reports anything on the Pi that interferes with the radio's
// serial port: at startup, then every 30 s while the radio is not connected
// (a login console is respawned with a new PID each time its session ends).
func watchPort(ctx context.Context, device string, radio *meshtastic.Client, log *slog.Logger) {
	var last string
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for first := true; ; first = false {
		if first || !radio.Status().Connected {
			problems := uartcheck.Check(device)
			if cur := strings.Join(problems, "\n"); cur != last {
				for _, p := range problems {
					log.Warn("serial port problem: "+p, "serial", device)
				}
				if len(problems) == 0 {
					log.Info("serial port problems cleared", "serial", device)
				}
				last = cur
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
