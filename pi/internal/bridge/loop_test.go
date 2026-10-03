package bridge

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/cot"
	"github.com/czediker/rar-atak-plugin/pi/internal/eud"
	"github.com/czediker/rar-atak-plugin/pi/internal/halow"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
	"github.com/czediker/rar-atak-plugin/pi/internal/state"
)

// TestLoopEndToEnd runs the real event loop with UDP sockets on localhost:
// a "phone" sends a chat to the auto port while isolated, the bridge
// forwards it to the radio, and a packet from the radio is delivered back
// to the phone's ATAK input port.
func TestLoopEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The phone's ATAK input socket.
	atak, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer atak.Close()
	deliver, err := NewUDPDeliverer(uint16(atak.LocalAddr().(*net.UDPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	defer deliver.Close()

	// Plugin listeners on ephemeral ports.
	inbound := make(chan Datagram, 8)
	autoAddr := freeUDPAddr(t)
	if err := ListenUDP(ctx, autoAddr, PortAuto, inbound, log); err != nil {
		t.Fatal(err)
	}

	radio := &fakeRadio{connected: true}
	radioRx := make(chan *meshpb.MeshPacket, 1)
	reports := make(chan halow.Report, 1)
	statePath := filepath.Join(t.TempDir(), "state.json")

	b := New(DefaultConfig(), radio, deliver, eud.NewRegistry(time.Minute), nil, log)
	done := make(chan struct{})
	go func() {
		b.Loop(ctx, LoopConfig{Inbound: inbound, Radio: radioRx, Reports: reports, StatePath: statePath})
		close(done)
	}()
	reports <- halow.Report{Link: halow.LinkIsolated}

	// Phone → bridge → radio.
	phone, err := net.Dial("udp4", autoAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	waitUntil(t, "isolated state", func() bool {
		s, err := state.Read(statePath)
		return err == nil && s.HaLow.State == state.HaLowIsolated
	})
	if _, err := phone.Write(chat("over lora")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "state shows tx", func() bool {
		s, err := state.Read(statePath)
		return err == nil && s.TxCount == 1 && s.EUDs == 1
	})
	if radio.sent[0].GetChat().GetMessage() != "over lora" {
		t.Errorf("radio got %v", radio.sent)
	}

	// Radio → bridge → phone's ATAK input.
	radioRx <- radioPacket(0x2222, 7, remotePLI("ANDROID-bravo"))
	atak.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	n, _, err := atak.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatalf("no delivery to ATAK: %v", err)
	}
	ev, err := cot.Parse(buf[:n])
	if err != nil || ev.UID != "ANDROID-bravo" {
		t.Fatalf("delivered %q (%v)", buf[:n], err)
	}

	cancel()
	<-done
}

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr := c.LocalAddr().String()
	c.Close()
	return addr
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDatagramSourceIsUnmapped(t *testing.T) {
	d := Datagram{Src: netip.MustParseAddr("::ffff:10.0.0.1").Unmap()}
	if !d.Src.Is4() {
		t.Error("expected IPv4")
	}
}
