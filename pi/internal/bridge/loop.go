package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/eud"
	"github.com/czediker/rar-atak-plugin/pi/internal/halow"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
	"github.com/czediker/rar-atak-plugin/pi/internal/state"
)

// Datagram is a packet received from the ATAK plugin.
type Datagram struct {
	Port Port
	Src  netip.Addr
	Data []byte
}

// ListenUDP receives plugin datagrams on addr (e.g. ":6700") and sends them
// to out until ctx is cancelled.
func ListenUDP(ctx context.Context, addr string, port Port, out chan<- Datagram, log *slog.Logger) error {
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return err
	}
	uc := conn.(*net.UDPConn)
	go func() {
		<-ctx.Done()
		uc.Close()
	}()
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, src, err := uc.ReadFromUDPAddrPort(buf)
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					log.Error("plugin listener failed", "addr", addr, "err", err)
				}
				return
			}
			log.Debug("UDP datagram from plugin", "listen", addr, "port", port, "src", src, "bytes", n)
			d := Datagram{Port: port, Src: src.Addr().Unmap(), Data: append([]byte(nil), buf[:n]...)}
			select {
			case out <- d:
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

// UDPDeliverer unicasts CoT events to EUDs on a fixed port (ATAK's default
// UDP input is 4242).
type UDPDeliverer struct {
	conn *net.UDPConn
	port uint16
}

// NewUDPDeliverer opens an unbound UDP socket for delivery.
func NewUDPDeliverer(port uint16) (*UDPDeliverer, error) {
	c, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	return &UDPDeliverer{conn: c, port: port}, nil
}

// Deliver sends data to addr and returns the destination "ip:port". UDP has
// no delivery receipt: success means the datagram left this host.
func (d *UDPDeliverer) Deliver(addr netip.Addr, data []byte) (string, error) {
	dst := netip.AddrPortFrom(addr, d.port)
	_, err := d.conn.WriteToUDPAddrPort(data, dst)
	return dst.String(), err
}

// Close releases the socket.
func (d *UDPDeliverer) Close() error { return d.conn.Close() }

// LoopConfig wires the bridge to its inputs.
type LoopConfig struct {
	Inbound <-chan Datagram
	Radio   <-chan *meshpb.MeshPacket
	// Acks carries the radio's QueueStatus messages (its confirmation of
	// each packet handed to it). Optional.
	Acks      <-chan *meshpb.QueueStatus
	Reports   <-chan halow.Report
	StatePath string
	// LeaseFile, if set, adds this node's DHCP clients as delivery targets.
	LeaseFile string
	Now       func() time.Time
}

// Loop drives the bridge until ctx is cancelled.
func (b *Bridge) Loop(ctx context.Context, lc LoopConfig) {
	now := lc.Now
	if now == nil {
		now = time.Now
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	stateTick := time.NewTicker(time.Second)
	defer stateTick.Stop()
	leaseTick := time.NewTicker(15 * time.Second)
	defer leaseTick.Stop()

	var written state.Snapshot
	first := true
	writeState := func() {
		if lc.StatePath == "" {
			return
		}
		snap := b.Snapshot(now())
		if err := state.Write(lc.StatePath, snap); err != nil {
			b.log.Warn("cannot write state file", "path", lc.StatePath, "err", err)
			return
		}
		if first {
			b.log.Debug("state.json written", "path", lc.StatePath, "halow", snap.HaLow.State, "halow_fault", snap.HaLow.Fault,
				"meshtastic_connected", snap.Meshtastic.Connected, "meshtastic_fault", snap.Meshtastic.Fault, "forwarding", snap.Forwarding)
		} else if diff := state.Diff(written, snap); len(diff) > 0 {
			b.log.Debug("state.json change committed", "changes", strings.Join(diff, "; "))
		}
		written, first = snap, false
	}
	var leases []netip.Addr
	leasesRead := false
	readLeases := func() {
		if lc.LeaseFile == "" {
			return
		}
		addrs, err := eud.ReadLeases(lc.LeaseFile, now())
		if err != nil {
			b.log.Debug("cannot read DHCP leases", "file", lc.LeaseFile, "err", err)
			return
		}
		if !leasesRead || !slices.Equal(addrs, leases) {
			b.log.Debug("DHCP clients of this node (also receive Meshtastic traffic)", "file", lc.LeaseFile, "addrs", fmt.Sprint(addrs))
		}
		leases, leasesRead = addrs, true
		b.euds.SetLeases(addrs)
	}
	readLeases()
	writeState()

	for {
		select {
		case <-ctx.Done():
			return
		case d := <-lc.Inbound:
			b.HandleEUD(d.Port, d.Src, d.Data, now())
		case p := <-lc.Radio:
			before := b.rxCount
			b.HandleRadio(p, now())
			if b.rxCount != before {
				writeState()
			}
		case qs := <-lc.Acks:
			b.HandleAck(qs, now())
		case r := <-lc.Reports:
			prev := b.link
			b.SetLink(r)
			if r.Link != prev {
				writeState()
			}
		case <-tick.C:
			before := b.txCount
			b.Tick(now())
			if b.txCount != before {
				writeState()
			}
		case <-stateTick.C:
			writeState()
		case <-leaseTick.C:
			readLeases()
		}
	}
}
