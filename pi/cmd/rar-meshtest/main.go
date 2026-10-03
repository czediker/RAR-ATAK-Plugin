// Command rar-meshtest checks the UART link between the Pi and the
// Meshtastic radio by broadcasting numbered text messages ("1" … "10") on
// the radio's channel, then exits.
//
// It proves both directions of the link: the radio must answer the config
// handshake (radio → Pi) and acknowledge every message it was handed
// (Pi → radio). The messages appear in that channel's chat on every other
// radio in range.
//
// Stop rar-bridge first; only one program can use the serial port:
//
//	/etc/init.d/rar-bridge stop
//	rar-meshtest
//	/etc/init.d/rar-bridge start
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshtastic"
)

var version = "dev"

func main() {
	var (
		serialDev = flag.String("serial", "/dev/ttyAMA0", "serial device connected to the RAK4631")
		baud      = flag.Int("baud", 115200, "serial baud rate (must match the Meshtastic Serial module)")
		channel   = flag.Uint("channel", 0, "channel index to send on (0 = the radio's primary channel)")
		count     = flag.Int("count", 10, "number of messages to send")
		interval  = flag.Duration("interval", 5*time.Second, "pause between messages")
		connectTO = flag.Duration("connect-timeout", 30*time.Second, "how long to wait for the radio to answer")
		verbose   = flag.Bool("v", false, "show client logs")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}

	level := slog.LevelError + 1 // silent unless -v
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Opening %s at %d baud...\n", *serialDev, *baud)
	opener := meshtastic.SerialOpener(*serialDev, *baud)
	// Fail fast if rar-bridge (or anything else) holds the port.
	if port, err := opener(); err != nil {
		if errors.Is(err, meshtastic.ErrPortBusy) {
			fmt.Printf("\nFAIL: %v.\n  Stop rar-bridge first: /etc/init.d/rar-bridge stop\n", err)
			os.Exit(2)
		}
		fmt.Printf("  (%v; will keep retrying for %s)\n", err, *connectTO)
	} else {
		port.Close()
	}

	info := newRadioInfo()
	client := meshtastic.New(meshtastic.Config{
		Channel:        uint32(*channel),
		ReconnectDelay: 2 * time.Second,
		Logger:         log,
		OnFromRadio:    info.observe,
	}, opener)
	go client.Run(ctx)

	os.Exit(run(ctx, client, info, options{
		Channel:        int32(*channel),
		Count:          *count,
		Interval:       *interval,
		ConnectTimeout: *connectTO,
		AckTimeout:     5 * time.Second,
		Drain:          5 * time.Second,
	}, os.Stdout))
}

type radio interface {
	SendData(port meshpb.PortNum, payload []byte) (uint32, error)
	Status() meshtastic.Status
}

type options struct {
	Channel        int32
	Count          int
	Interval       time.Duration
	ConnectTimeout time.Duration
	AckTimeout     time.Duration
	Drain          time.Duration
}

// Firmware error codes reported in QueueStatus.res.
const (
	errnoOK            = 0
	errnoShouldRelease = 35 // "no error, caller frees"
)

// radioInfo collects what the radio reports during the run.
type radioInfo struct {
	mu       sync.Mutex
	firmware string
	preset   string
	channels map[int32]string
	acks     map[uint32]*meshpb.QueueStatus
}

func newRadioInfo() *radioInfo {
	return &radioInfo{channels: map[int32]string{}, acks: map[uint32]*meshpb.QueueStatus{}}
}

func (r *radioInfo) observe(m *meshpb.FromRadio) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch v := m.GetPayloadVariant().(type) {
	case *meshpb.FromRadio_Metadata:
		r.firmware = v.Metadata.GetFirmwareVersion()
	case *meshpb.FromRadio_Config:
		if lora := v.Config.GetLora(); lora != nil {
			r.preset = lora.GetModemPreset().String()
		}
	case *meshpb.FromRadio_Channel:
		ch := v.Channel
		if ch.GetRole() == meshpb.Channel_DISABLED {
			return
		}
		name := ch.GetSettings().GetName()
		if name == "" {
			name = "default name"
		}
		r.channels[ch.GetIndex()] = fmt.Sprintf("%s, %s", name, ch.GetRole())
	case *meshpb.FromRadio_QueueStatus:
		if id := v.QueueStatus.GetMeshPacketId(); id != 0 {
			r.acks[id] = v.QueueStatus
		}
	}
}

func (r *radioInfo) ack(id uint32) *meshpb.QueueStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acks[id]
}

// run performs the test and returns the process exit code: 0 when every
// message was accepted by the radio, 1 when some were not, 2 when the radio
// never answered.
func run(ctx context.Context, r radio, info *radioInfo, opt options, out io.Writer) int {
	if !waitFor(ctx, opt.ConnectTimeout, func() bool { return r.Status().Connected }) {
		st := r.Status()
		fmt.Fprintf(out, "\nFAIL: no answer from the radio within %s.\n", opt.ConnectTimeout)
		if st.LastError != "" {
			fmt.Fprintf(out, "  Last error: %s\n", st.LastError)
		}
		fmt.Fprint(out, `  Check:
  - rar-bridge is stopped (/etc/init.d/rar-bridge stop); only one program can use the port
  - the RAK4631 Serial module is enabled in PROTO mode with rxd 15 / txd 16
  - the baud rate matches (-baud) and Pi TX/RX go to RAK RXD1/TXD1 (crossed)
  - no Linux console or login is running on the UART (cmdline.txt, /etc/inittab)
`)
		return 2
	}

	st := r.Status()
	info.mu.Lock()
	fw, preset, chName := info.firmware, info.preset, info.channels[opt.Channel]
	info.mu.Unlock()
	fmt.Fprintf(out, "Radio answered: node %s", meshtastic.NodeID(st.NodeNum))
	if fw != "" {
		fmt.Fprintf(out, ", firmware %s", fw)
	}
	fmt.Fprintf(out, ", hop limit %d", st.DeviceHopLimit)
	if preset != "" {
		fmt.Fprintf(out, ", preset %s", preset)
	}
	fmt.Fprintln(out)
	if chName == "" {
		chName = "not reported by the radio"
	}
	fmt.Fprintf(out, "Sending %d text messages on channel %d (%s), %s apart\n\n", opt.Count, opt.Channel, chName, opt.Interval)

	accepted := 0
	for i := 1; i <= opt.Count; i++ {
		text := strconv.Itoa(i)
		fmt.Fprintf(out, "[%2d/%d] %q ", i, opt.Count, text)
		id, err := r.SendData(meshpb.PortNum_TEXT_MESSAGE_APP, []byte(text))
		switch {
		case err != nil:
			fmt.Fprintf(out, "NOT SENT: %v\n", err)
		default:
			var qs *meshpb.QueueStatus
			waitFor(ctx, opt.AckTimeout, func() bool { qs = info.ack(id); return qs != nil })
			switch {
			case qs == nil:
				fmt.Fprintf(out, "sent (id %08x), but the radio did not confirm it\n", id)
			case qs.GetRes() == errnoOK || qs.GetRes() == errnoShouldRelease:
				accepted++
				fmt.Fprintf(out, "accepted by the radio (id %08x, TX queue %d/%d free)\n", id, qs.GetFree(), qs.GetMaxlen())
			default:
				fmt.Fprintf(out, "REJECTED by the radio (error %d, TX queue %d/%d free)\n", qs.GetRes(), qs.GetFree(), qs.GetMaxlen())
			}
		}
		if ctx.Err() != nil {
			fmt.Fprintln(out, "Interrupted.")
			return 1
		}
		if i < opt.Count {
			sleep(ctx, opt.Interval)
		}
	}

	fmt.Fprintf(out, "\nWaiting %s for the radio to finish transmitting...\n", opt.Drain)
	sleep(ctx, opt.Drain)
	if accepted == opt.Count {
		fmt.Fprintf(out, "PASS: all %d messages were accepted by the radio. Check that they appear on the other radios.\n", opt.Count)
		return 0
	}
	fmt.Fprintf(out, "FAIL: %d of %d messages were accepted by the radio.\n", accepted, opt.Count)
	return 1
}

// waitFor polls cond until it is true, the timeout passes or ctx ends.
func waitFor(ctx context.Context, timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		sleep(ctx, 50*time.Millisecond)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
