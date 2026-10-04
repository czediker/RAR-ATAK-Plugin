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
//
// rar-meshtest -loopback tests the Pi's UART on its own: unplug the radio and
// connect Pi pin 8 straight to pin 10.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshtastic"
	"github.com/czediker/rar-atak-plugin/pi/internal/uartcheck"
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
		loop      = flag.Bool("loopback", false, "test only the Pi's UART: radio unplugged, Pi pin 8 wired to pin 10")
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
	port, err := opener()
	switch {
	case errors.Is(err, meshtastic.ErrPortBusy):
		fmt.Printf("\nFAIL: %v.\n  Stop rar-bridge first: /etc/init.d/rar-bridge stop\n", err)
		os.Exit(2)
	case err != nil && *loop:
		fmt.Printf("\nFAIL: %v\n", err)
		os.Exit(2)
	case err != nil:
		fmt.Printf("  (%v; will keep retrying for %s)\n", err, *connectTO)
	case *loop:
		os.Exit(loopback(port, 2*time.Second, uartcheck.Check(*serialDev), os.Stdout))
	default:
		port.Close()
	}

	info := newRadioInfo()
	client := meshtastic.New(meshtastic.Config{
		Channel:        uint32(*channel),
		ReconnectDelay: 2 * time.Second,
		Logger:         log,
		OnFromRadio:    info.observe,
	}, opener)
	clientDone := make(chan struct{})
	go func() {
		client.Run(ctx)
		close(clientDone)
	}()

	code := run(ctx, client, info, options{
		Channel:        int32(*channel),
		Count:          *count,
		Interval:       *interval,
		ConnectTimeout: *connectTO,
		AckTimeout:     5 * time.Second,
		Drain:          5 * time.Second,
		PortProblems:   func() []string { return uartcheck.Check(*serialDev) },
	}, os.Stdout)

	// Stopping the client tells the radio this program is leaving, so the
	// radio ends the serial session (and turns Bluetooth back on) at once.
	stop()
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
	}
	os.Exit(code)
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
	// PortProblems, if set, reports things on the Pi that interfere with
	// the serial port. Called only when the radio does not answer.
	PortProblems func() []string
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
		if !strings.HasPrefix(st.LastError, "open") && !strings.Contains(st.LastError, "hung up") {
			// The port opened: what came back says which side to look at.
			fmt.Fprintf(out, "  Received from the radio: %d bytes, %d API frames\n  -> %s\n",
				st.RxBytes, st.RxFrames, meshtastic.LinkHint(st.RxBytes, st.RxFrames))
		}
		if opt.PortProblems != nil {
			if problems := opt.PortProblems(); len(problems) > 0 {
				fmt.Fprintln(out, "  Problems found on this Pi:")
				for _, p := range problems {
					fmt.Fprintf(out, "  - %s\n", p)
				}
			}
		}
		fmt.Fprint(out, `  Check:
  - the RAK4631 Serial module is enabled in PROTO mode with rxd 15 / txd 16
  - the RAK4631 GPS mode is NOT_PRESENT (its GPS driver uses the same pins)
  - the baud rate matches (-baud) and Pi TX/RX go to RAK RXD1/TXD1 (crossed), grounds joined
  - no Linux console or login is running on the UART (cmdline.txt, /etc/inittab)
  - the Pi's UART on its own: rar-meshtest -loopback (radio unplugged, pin 8 wired to pin 10)
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

// loopback checks the Pi's UART on its own, with pin 8 (TXD) wired straight
// to pin 10 (RXD) and the radio unplugged: what is sent must come straight
// back. It closes port and returns the exit code.
func loopback(port io.ReadWriteCloser, timeout time.Duration, problems []string, out io.Writer) int {
	pattern := []byte("rar-meshtest loopback 0123456789 ABCDEFGHIJKLMNOPQRSTUVWXYZ\n")
	fmt.Fprintln(out, "Loopback test: the radio must be unplugged and Pi pin 8 wired to pin 10.")
	got := make(chan []byte, 1)
	go func() {
		var buf []byte
		tmp := make([]byte, 256)
		for !bytes.Contains(buf, pattern) && len(buf) < 4096 {
			n, err := port.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				break
			}
		}
		got <- buf
	}()

	_, werr := port.Write(pattern)
	var buf []byte
	select {
	case buf = <-got:
	case <-time.After(timeout):
		port.Close() // unblocks the reader
		buf = <-got
	}
	port.Close()

	if len(problems) > 0 {
		fmt.Fprintln(out, "Problems found on this Pi:")
		for _, p := range problems {
			fmt.Fprintf(out, "  - %s\n", p)
		}
	}
	switch {
	case werr != nil:
		fmt.Fprintf(out, "FAIL: could not write to the port: %v\n", werr)
		return 2
	case bytes.Contains(buf, pattern):
		fmt.Fprint(out, `PASS: the Pi's UART sends on pin 8 and receives on pin 10.
  Reconnect the radio: Pi pin 8 -> RAK RXD1, Pi pin 10 <- RAK TXD1, ground to ground.
  If the radio still does not answer, the problem is on the radio side
  (Serial module settings, GPS mode NOT_PRESENT, the RAK TXD1 wire).
`)
		return 0
	case len(buf) == 0:
		fmt.Fprint(out, `FAIL: nothing came back.
  With pins 8 and 10 joined, this means the jumper is not on those pins, or the
  UART is not routed to them: config.txt needs enable_uart=1 and
  dtoverlay=disable-bt (reboot after changing it).
`)
		return 1
	default:
		fmt.Fprintf(out, `FAIL: %d bytes came back, but not what was sent:
  %q
  Another program may be using the port (a login console echoes and adds text),
  or the jumper is loose.
`, len(buf), truncate(buf, 120))
		return 1
	}
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
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
