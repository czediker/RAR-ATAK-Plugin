// Package bridge implements the forwarding policy between the ATAK plugin
// (UDP ports 6700/6701), the HaLow link state, and the Meshtastic radio.
//
// Outbound (EUD → Meshtastic):
//   - Port 6701 ("always"): every chat and position report is forwarded.
//   - Port 6700 ("auto"): forwarded only while the HaLow link is isolated
//     (or not yet known).
//
// Inbound (Meshtastic → EUD): every ATAK_PLUGIN TAKPacket is converted back
// to CoT and unicast to this radio's own EUD(s).
//
// The Bridge type holds all state and is driven from a single goroutine
// (see Loop), so it needs no locking.
//
// Everything the bridge decides is logged at debug level (rar-bridge
// -debug) so a message can be followed from the plugin to the radio and
// back.
package bridge

import (
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"slices"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/cot"
	"github.com/czediker/rar-atak-plugin/pi/internal/eud"
	"github.com/czediker/rar-atak-plugin/pi/internal/halow"
	"github.com/czediker/rar-atak-plugin/pi/internal/halowseen"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshtastic"
	"github.com/czediker/rar-atak-plugin/pi/internal/state"
	"github.com/czediker/rar-atak-plugin/pi/internal/takconv"
)

// Port identifies which plugin port a datagram arrived on.
type Port int

const (
	// PortAuto (6700) traffic is forwarded only while HaLow is isolated.
	PortAuto Port = iota
	// PortAlways (6701) traffic is always forwarded.
	PortAlways
)

func (p Port) String() string {
	if p == PortAlways {
		return "always"
	}
	return "auto"
}

// Radio is the Meshtastic side; *meshtastic.Client implements it.
type Radio interface {
	SendData(port meshpb.PortNum, payload []byte) (uint32, error)
	Status() meshtastic.Status
}

// Deliverer sends a CoT event to a local EUD and returns the destination
// it used (ip:port) for logging.
type Deliverer interface {
	Deliver(addr netip.Addr, data []byte) (dest string, err error)
}

// Config holds the bridge policy settings.
type Config struct {
	// PLIInterval is the normal minimum spacing of position reports per
	// user over Meshtastic.
	PLIInterval time.Duration
	// PLIMinInterval is the minimum spacing when the user has moved at
	// least PLIMoveMeters since the last report sent.
	PLIMinInterval time.Duration
	PLIMoveMeters  float64
	// TxGap is the minimum time between any two Meshtastic transmissions.
	TxGap time.Duration
	// ChatQueueMax bounds the outgoing chat queue (oldest dropped first).
	ChatQueueMax int
	// ChatMaxAge drops chats that could not be sent in time.
	ChatMaxAge time.Duration
	// ForcedWindow is how long after the last port-6701 packet the
	// "forced" status stays on.
	ForcedWindow time.Duration
	// PLIStale and ChatStale set the stale time of rebuilt CoT events.
	PLIStale  time.Duration
	ChatStale time.Duration
	// SuppressHaLowDupes drops Meshtastic traffic from users currently
	// heard over HaLow (the EUD already has it).
	SuppressHaLowDupes bool
	// DedupeWindow is how long received packet IDs are remembered.
	DedupeWindow time.Duration
	// AckTimeout is how long to wait for the radio to confirm a packet.
	AckTimeout time.Duration
	// MeshFaultHold keeps the Meshtastic fault flag set this long after a
	// failed, rejected or unconfirmed packet.
	MeshFaultHold time.Duration
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		PLIInterval:        60 * time.Second,
		PLIMinInterval:     15 * time.Second,
		PLIMoveMeters:      50,
		TxGap:              3 * time.Second,
		ChatQueueMax:       32,
		ChatMaxAge:         10 * time.Minute,
		ForcedWindow:       90 * time.Second,
		PLIStale:           6 * time.Minute,
		ChatStale:          24 * time.Hour,
		SuppressHaLowDupes: true,
		DedupeWindow:       10 * time.Minute,
		AckTimeout:         10 * time.Second,
		MeshFaultHold:      30 * time.Second,
	}
}

type queuedChat struct {
	payload  []byte
	enqueued time.Time
	failures int
	desc     string
	uid      string
}

type queuedPLI struct {
	payload  []byte
	lat, lon float64
	port     Port
	enqueued time.Time
	callsign string
}

type sentPLI struct {
	at       time.Time
	lat, lon float64
}

// inFlight is a packet handed to the radio, awaiting its QueueStatus.
type inFlight struct {
	kind, who, uid string
	bytes          int
	at             time.Time
}

type rxKey struct{ from, id uint32 }

type position struct {
	pos takconv.Position
	at  time.Time
}

// Bridge holds the forwarding state. Create it with New.
type Bridge struct {
	cfg     Config
	log     *slog.Logger
	radio   Radio
	deliver Deliverer
	euds    *eud.Registry
	seen    *halowseen.Tracker // nil disables HaLow duplicate suppression

	link      halow.Link
	neighbors int
	linkErr   string

	chats   []queuedChat
	plis    map[string]*queuedPLI
	lastPLI map[string]sentPLI
	lastTx  time.Time

	inFlight      map[uint32]inFlight
	meshErr       string
	meshErrAt     time.Time
	radioWasReady bool
	waitingLogged bool

	lastForced time.Time
	senders    map[string]takconv.Sender
	positions  map[string]position
	rxSeen     map[rxKey]time.Time
	targets    []netip.Addr

	// clock follows the EUD's clock; CoT delivered to ATAK carries its time.
	clock      eudClock
	skewLogged time.Duration
	// compressed holds compressed TAKPackets waiting for the radio's
	// decompressed copy; undecompressed counts those that never got one.
	compressed       map[rxKey]time.Time
	undecompressed   int
	compressedWarnAt time.Time

	txCount, rxCount uint64
	lastTxAt         time.Time
	lastRxAt         time.Time
}

// New creates a bridge. seen may be nil.
func New(cfg Config, radio Radio, deliver Deliverer, euds *eud.Registry, seen *halowseen.Tracker, log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.Default()
	}
	return &Bridge{
		cfg: cfg, log: log.With("component", "bridge"),
		radio: radio, deliver: deliver, euds: euds, seen: seen,
		plis:       map[string]*queuedPLI{},
		lastPLI:    map[string]sentPLI{},
		inFlight:   map[uint32]inFlight{},
		senders:    map[string]takconv.Sender{},
		positions:  map[string]position{},
		rxSeen:     map[rxKey]time.Time{},
		compressed: map[rxKey]time.Time{},
	}
}

// Forwarding reports whether port-6700 traffic currently goes to
// Meshtastic. Until the first neighbor check the answer is yes, so a
// radio that boots out of range is never silent.
func (b *Bridge) Forwarding() bool { return b.link != halow.LinkConnected }

// SetLink records a HaLow monitor report.
func (b *Bridge) SetLink(r halow.Report) {
	if r.Link != b.link && r.Link == halow.LinkConnected {
		b.log.Info("HaLow neighbors present; port 6700 traffic stays on HaLow", "neighbors", r.Neighbors)
	} else if r.Link != b.link && r.Link == halow.LinkIsolated {
		b.log.Info("no HaLow neighbors; FALLOVER: forwarding port 6700 traffic over Meshtastic")
	}
	b.link, b.neighbors = r.Link, r.Neighbors
	b.linkErr = ""
	if r.Err != nil {
		b.linkErr = r.Err.Error()
	}
}

// HandleEUD processes a CoT datagram from the ATAK plugin.
func (b *Bridge) HandleEUD(port Port, src netip.Addr, data []byte, now time.Time) {
	ev, err := cot.Parse(data)
	if err != nil {
		b.log.Debug("plugin datagram is not CoT; ignored", "port", port, "src", src, "bytes", len(data), "err", err, "start", preview(data))
		return
	}
	uid := ev.UID
	if ev.IsChat() {
		uid = ev.Chat.SenderUID
	}
	b.clock.observe(ev.Time, now)
	b.noteClockSkew(now)
	if newAddr, newUID := b.euds.Learn(src, uid, now); newAddr || newUID {
		b.log.Info("local EUD learned from plugin traffic", "addr", src, "uid", uid, "new_addr", newAddr, "new_uid", newUID)
	}
	if port == PortAlways {
		b.lastForced = now
	}
	if ev.IsPLI() {
		b.senders[ev.UID] = takconv.Sender{Team: ev.Team, Role: ev.Role, Battery: ev.Battery}
	}
	forward := port == PortAlways || b.Forwarding()
	b.log.Debug("plugin message received", append([]any{"port", port, "src", src, "cot_bytes", len(data), "forward", forward}, eventAttrs(ev)...)...)

	if !ev.IsChat() && !ev.IsPLI() {
		b.log.Debug("CoT type not carried over Meshtastic; ignored", "type", ev.Type, "uid", ev.UID)
		return
	}
	if !forward {
		b.log.Debug("not sent to Meshtastic: HaLow has neighbors and this arrived on the auto port", "uid", uid)
		return
	}

	switch {
	case ev.IsChat():
		var sender *takconv.Sender
		if s, ok := b.senders[ev.Chat.SenderUID]; ok {
			sender = &s
		}
		payload, err := b.encode(ev, sender)
		if err != nil {
			return
		}
		if len(b.chats) >= b.cfg.ChatQueueMax {
			b.log.Warn("chat queue full; dropping oldest unsent chat", "desc", b.chats[0].desc)
			b.chats = b.chats[1:]
		}
		desc := ev.Chat.SenderCallsign + " → " + ev.Chat.ToUID
		b.chats = append(b.chats, queuedChat{payload: payload, enqueued: now, desc: desc, uid: uid})
		b.log.Debug("chat queued for Meshtastic", "desc", desc, "takpacket_bytes", len(payload), "chats_waiting", len(b.chats))
	case ev.IsPLI():
		payload, err := b.encode(ev, nil)
		if err != nil {
			return
		}
		if old, ok := b.plis[ev.UID]; ok {
			b.log.Debug("newer position replaces an unsent queued one (only the latest is sent)", "uid", ev.UID, "replaced_age", now.Sub(old.enqueued).Round(time.Millisecond))
		}
		q := &queuedPLI{payload: payload, lat: ev.Lat, lon: ev.Lon, port: port, enqueued: now, callsign: ev.Callsign}
		b.plis[ev.UID] = q
		attrs := []any{"uid", ev.UID, "takpacket_bytes", len(payload)}
		if wait := b.pliWait(ev.UID, q, now); wait > 0 {
			attrs = append(attrs, "rate_limited_for", wait.Round(time.Second))
		}
		b.log.Debug("position queued for Meshtastic", attrs...)
	}
}

func (b *Bridge) encode(ev *cot.Event, sender *takconv.Sender) ([]byte, error) {
	pkt, err := takconv.FromEvent(ev, sender)
	if err == nil {
		textBefore := pkt.GetChat().GetMessage()
		var payload []byte
		if payload, err = takconv.Marshal(pkt); err == nil {
			if c := pkt.GetChat(); c != nil && len(c.GetMessage()) < len(textBefore) {
				b.log.Warn("chat text truncated to fit a Meshtastic packet", "uid", ev.UID, "from_bytes", len(textBefore), "to_bytes", len(c.GetMessage()))
			}
			b.log.Debug("converted to TAKPacket", append([]any{"takpacket_bytes", len(payload), "max", takconv.MaxPayload}, packetAttrs(pkt)...)...)
			return payload, nil
		}
	}
	b.log.Warn("cannot convert CoT to TAKPacket; not sent", "uid", ev.UID, "type", ev.Type, "err", err)
	return nil, err
}

// Tick expires old state and transmits at most one queued packet.
func (b *Bridge) Tick(now time.Time) {
	b.expire(now)
	b.checkAcks(now)
	b.checkCompressed(now)
	b.logTargets(now)

	st := b.radio.Status()
	if st.Connected != b.radioWasReady {
		if st.Connected {
			b.log.Info("Meshtastic radio ready", "node", meshtastic.NodeID(st.NodeNum))
		} else {
			b.log.Warn("Meshtastic radio not available", "err", st.LastError)
		}
		b.radioWasReady = st.Connected
	}
	if !st.Connected {
		if (len(b.chats) > 0 || len(b.plis) > 0) && !b.waitingLogged {
			b.log.Debug("messages waiting for the Meshtastic radio", "chats", len(b.chats), "positions", len(b.plis))
			b.waitingLogged = true
		}
		return
	}
	b.waitingLogged = false
	if !b.lastTx.IsZero() && now.Sub(b.lastTx) < b.cfg.TxGap {
		return
	}
	if len(b.chats) > 0 {
		c := &b.chats[0]
		if b.send(c.payload, now, "chat", c.desc, c.uid) {
			b.chats = b.chats[1:]
		} else if c.failures++; c.failures >= 3 {
			b.log.Warn("giving up on chat after repeated send failures", "desc", c.desc)
			b.chats = b.chats[1:]
		}
		return
	}
	if uid, q := b.nextPLI(now); q != nil {
		if b.send(q.payload, now, "position", q.callsign, uid) {
			delete(b.plis, uid)
			b.lastPLI[uid] = sentPLI{at: now, lat: q.lat, lon: q.lon}
		}
	}
}

// nextPLI returns the oldest queued position report that is due.
func (b *Bridge) nextPLI(now time.Time) (string, *queuedPLI) {
	var bestUID string
	var best *queuedPLI
	for uid, q := range b.plis {
		if b.pliWait(uid, q, now) > 0 {
			continue
		}
		if best == nil || q.enqueued.Before(best.enqueued) {
			bestUID, best = uid, q
		}
	}
	return bestUID, best
}

// pliWait returns how long the position for uid must still wait (0 = due).
func (b *Bridge) pliWait(uid string, q *queuedPLI, now time.Time) time.Duration {
	last, ok := b.lastPLI[uid]
	if !ok {
		return 0
	}
	elapsed := now.Sub(last.at)
	wait := b.cfg.PLIInterval - elapsed
	if distanceMeters(last.lat, last.lon, q.lat, q.lon) >= b.cfg.PLIMoveMeters {
		wait = min(wait, b.cfg.PLIMinInterval-elapsed)
	}
	return max(wait, 0)
}

func (b *Bridge) send(payload []byte, now time.Time, kind, who, uid string) bool {
	b.lastTx = now
	id, err := b.radio.SendData(meshpb.PortNum_ATAK_PLUGIN, payload)
	if err != nil {
		b.meshFault(now, "send failed: "+err.Error())
		b.log.Warn("FAILED to pass message to Meshtastic radio", "kind", kind, "who", who, "uid", uid, "err", err)
		return false
	}
	b.txCount++
	b.lastTxAt = now
	b.inFlight[id] = inFlight{kind: kind, who: who, uid: uid, bytes: len(payload), at: now}
	b.log.Info("passed to Meshtastic radio", "kind", kind, "who", who, "uid", uid, "bytes", len(payload), "packet_id", id)
	return true
}

// HandleAck processes the radio's QueueStatus for a packet we sent.
func (b *Bridge) HandleAck(qs *meshpb.QueueStatus, now time.Time) {
	id := qs.GetMeshPacketId()
	f, ok := b.inFlight[id]
	if !ok {
		if id != 0 {
			b.log.Debug("radio queue status for a packet not sent by the bridge", "packet_id", id, "res", qs.GetRes())
		}
		return
	}
	delete(b.inFlight, id)
	attrs := []any{"kind", f.kind, "who", f.who, "uid", f.uid, "packet_id", id, "queue_free", qs.GetFree(), "queue_max", qs.GetMaxlen(), "after", now.Sub(f.at).Round(time.Millisecond)}
	// 0 = OK; 35 = "no error, caller frees" (see the firmware's MeshTypes.h).
	if res := qs.GetRes(); res != 0 && res != 35 {
		b.meshFault(now, fmt.Sprintf("radio rejected packet %08x (error %d)", id, res))
		b.log.Warn("Meshtastic radio REJECTED the message", append(attrs, "error_code", res)...)
		return
	}
	b.log.Debug("Meshtastic radio accepted the message for transmission", attrs...)
}

func (b *Bridge) checkAcks(now time.Time) {
	for id, f := range b.inFlight {
		if now.Sub(f.at) > b.cfg.AckTimeout {
			delete(b.inFlight, id)
			b.meshFault(now, fmt.Sprintf("radio did not confirm packet %08x", id))
			b.log.Warn("Meshtastic radio did not confirm the message", "kind", f.kind, "who", f.who, "uid", f.uid, "packet_id", id, "waited", b.cfg.AckTimeout)
		}
	}
}

func (b *Bridge) meshFault(now time.Time, msg string) {
	b.meshErr, b.meshErrAt = msg, now
}

func (b *Bridge) expire(now time.Time) {
	for len(b.chats) > 0 && now.Sub(b.chats[0].enqueued) > b.cfg.ChatMaxAge {
		b.log.Warn("dropping chat that could not be sent in time", "desc", b.chats[0].desc, "max_age", b.cfg.ChatMaxAge)
		b.chats = b.chats[1:]
	}
	for uid, q := range b.plis {
		// Position reports that only needed Meshtastic because HaLow was
		// down are stale once HaLow is back.
		if q.port == PortAuto && !b.Forwarding() {
			b.log.Debug("dropping queued position: HaLow is back", "uid", uid)
			delete(b.plis, uid)
		}
	}
	for k, t := range b.rxSeen {
		if now.Sub(t) > b.cfg.DedupeWindow {
			delete(b.rxSeen, k)
		}
	}
	for uid, p := range b.positions {
		if now.Sub(p.at) > time.Hour {
			delete(b.positions, uid)
		}
	}
	for uid, s := range b.lastPLI {
		if now.Sub(s.at) > time.Hour {
			delete(b.lastPLI, uid)
		}
	}
}

// logTargets reports changes to the set of EUDs that receive Meshtastic
// traffic.
func (b *Bridge) logTargets(now time.Time) {
	t := b.euds.Targets(now)
	if slices.Equal(t, b.targets) {
		return
	}
	b.log.Debug("EUD delivery targets changed", "targets", fmt.Sprint(t), "previous", fmt.Sprint(b.targets))
	b.targets = t
}

// HandleRadio processes a packet received from the Meshtastic radio.
func (b *Bridge) HandleRadio(p *meshpb.MeshPacket, now time.Time) {
	d := p.GetDecoded()
	if d == nil {
		b.log.Debug("Meshtastic packet could not be decrypted by the radio (different channel/key); ignored", meshAttrs(p)...)
		return
	}
	attrs := append(meshAttrs(p), "portnum", d.GetPortnum(), "payload_bytes", len(d.GetPayload()))
	if d.GetPortnum() != meshpb.PortNum_ATAK_PLUGIN {
		b.log.Debug("Meshtastic packet is not ATAK traffic; ignored", attrs...)
		return
	}
	key := rxKey{p.GetFrom(), p.GetId()}
	if first, dup := b.rxSeen[key]; dup {
		b.log.Debug("DUPLICATE Meshtastic packet; already handled", append(attrs, "first_seen_ago", now.Sub(first).Round(time.Millisecond))...)
		return
	}
	pkt, err := takconv.Decode(d.GetPayload())
	if err != nil {
		b.log.Warn("bad TAKPacket from Meshtastic", append(attrs, "err", err)...)
		return
	}
	b.log.Debug("ATAK data received from Meshtastic", append(attrs, packetAttrs(pkt)...)...)
	if pkt.GetIsCompressed() {
		// Firmware up to 2.7 compresses TAKPacket strings (unishox2) on
		// radios whose role is TAK. Such a radio also hands its clients a
		// decompressed copy with the same packet ID, so wait for that;
		// checkCompressed warns if it never comes (role not TAK).
		if _, ok := b.compressed[key]; !ok {
			b.compressed[key] = now
		}
		b.log.Debug("compressed TAKPacket held back; waiting for the radio's decompressed copy", "from", meshtastic.NodeID(p.GetFrom()), "packet_id", p.GetId())
		return
	}
	delete(b.compressed, key)
	b.rxSeen[key] = now

	uid := takconv.SenderUID(pkt)
	if uid == "" {
		b.log.Debug("TAKPacket has no sender UID; ignored", "from", meshtastic.NodeID(p.GetFrom()))
		return
	}
	if b.euds.IsLocalUID(uid, now) {
		b.log.Debug("TAKPacket is from this radio's own EUD; ignored (DEDUPE)", "uid", uid)
		return
	}
	b.rxCount++
	b.lastRxAt = now
	if pli := pkt.GetPli(); pli != nil {
		pos := takconv.Position{Lat: float64(pli.GetLatitudeI()) / 1e7, Lon: float64(pli.GetLongitudeI()) / 1e7, HAE: cot.Unknown}
		if alt := pli.GetAltitude(); alt != 0 {
			pos.HAE = float64(alt)
		}
		b.positions[uid] = position{pos: pos, at: now}
	}
	if b.cfg.SuppressHaLowDupes && b.seen != nil && b.seen.Recently(uid, now) {
		last, _ := b.seen.LastHeard(uid)
		b.log.Debug("DEDUPE: sender is reachable over HaLow, so the EUD already has this; Meshtastic copy dropped", "uid", uid, "heard_over_halow_ago", now.Sub(last).Round(time.Second))
		return
	}
	if chat := pkt.GetChat(); chat != nil {
		to := chat.GetTo()
		if to != "" && to != cot.AllChatRooms {
			if local := b.euds.LocalUIDs(now); len(local) > 0 && !slices.Contains(local, to) {
				b.log.Debug("direct message for another user; not delivered", "to", to, "local_uids", fmt.Sprint(local))
				return
			}
		}
	}

	x, err := takconv.ToCoT(pkt, takconv.CoTOptions{
		Now:       b.clock.now(now), // ATAK orders chat and judges staleness by this
		PLIStale:  b.cfg.PLIStale,
		ChatStale: b.cfg.ChatStale,
		Via:       cot.Via{Transport: "meshtastic", Node: meshtastic.NodeID(p.GetFrom())},
		FromNode:  p.GetFrom(),
		PacketID:  p.GetId(),
		SenderPos: func(uid string) (takconv.Position, bool) {
			pp, ok := b.positions[uid]
			return pp.pos, ok
		},
	})
	if err != nil {
		b.log.Debug("TAKPacket type not converted to CoT; ignored", "from", meshtastic.NodeID(p.GetFrom()), "err", err)
		return
	}
	kind := kindOf(pkt)
	targets := b.euds.Targets(now)
	if len(targets) == 0 {
		b.log.Warn("received over Meshtastic but no local EUD is known yet; not delivered", "kind", kind, "uid", uid)
		return
	}
	delivered := 0
	for _, a := range targets {
		dest, err := b.deliver.Deliver(a, x)
		if err != nil {
			b.log.Warn("FAILED to send to ATAK", "kind", kind, "uid", uid, "dest", dest, "err", err)
			continue
		}
		delivered++
		b.log.Debug("sent to ATAK", "kind", kind, "uid", uid, "dest", dest, "cot_bytes", len(x))
	}
	b.log.Info("received over Meshtastic and sent to ATAK", "kind", kind, "callsign", pkt.GetContact().GetCallsign(), "uid", uid, "from", meshtastic.NodeID(p.GetFrom()), "euds", delivered, "of", len(targets))
}

const (
	// compressedWait is how long a compressed TAKPacket waits for the
	// radio's decompressed copy.
	compressedWait = 3 * time.Second
	// compressedWarnEvery limits the warning about undecompressed packets.
	compressedWarnEvery = 5 * time.Minute
	// clockSkewReport is how far the EUD's and this host's clocks may be
	// apart (or the gap change) before it is logged.
	clockSkewReport = 30 * time.Second
)

// checkCompressed reports compressed TAKPackets the radio never decompressed:
// this bridge cannot read them, and the radio only decompresses when its
// device role is TAK.
func (b *Bridge) checkCompressed(now time.Time) {
	n := 0
	for k, at := range b.compressed {
		if now.Sub(at) >= compressedWait {
			delete(b.compressed, k)
			n++
		}
	}
	if n == 0 {
		return
	}
	b.undecompressed += n
	b.meshFault(now, "compressed ATAK data not decompressed by the radio: set its device role to TAK")
	if !b.compressedWarnAt.IsZero() && now.Sub(b.compressedWarnAt) < compressedWarnEvery {
		return
	}
	b.compressedWarnAt = now
	b.log.Warn("received compressed ATAK data the radio did not decompress, so it cannot be delivered; "+
		"set the radio's device role to TAK (meshtastic --set device.role TAK)", "packets", b.undecompressed)
	b.undecompressed = 0
}

// noteClockSkew logs when this host's clock and the EUD's are far apart, or
// the gap changes: CoT delivered to ATAK is stamped with the EUD's time.
func (b *Bridge) noteClockSkew(now time.Time) {
	off, ok := b.clock.offset(now)
	if !ok {
		return
	}
	if d := off - b.skewLogged; d > -clockSkewReport && d < clockSkewReport {
		return
	}
	b.skewLogged = off
	if off > -clockSkewReport && off < clockSkewReport {
		b.log.Info("this radio's clock now matches the EUD's", "eud_ahead_by", off.Round(time.Second))
		return
	}
	b.log.Warn("this radio's clock differs from the EUD's; traffic delivered to ATAK is stamped with the EUD's time",
		"eud_ahead_by", off.Round(time.Second),
		"radio_time", now.UTC().Format(time.RFC3339), "eud_time", now.Add(off).UTC().Format(time.RFC3339))
}

// Snapshot returns the status for the LED service.
func (b *Bridge) Snapshot(now time.Time) state.Snapshot {
	var s state.Snapshot
	s.Updated = now
	switch b.link {
	case halow.LinkConnected:
		s.HaLow.State = state.HaLowConnected
	case halow.LinkIsolated:
		s.HaLow.State = state.HaLowIsolated
	default:
		s.HaLow.State = state.HaLowUnknown
	}
	s.HaLow.Neighbors = b.neighbors
	halowErr := b.linkErr
	if b.seen != nil {
		if le := b.seen.ListenerError(); le != "" {
			if halowErr != "" {
				halowErr += "; "
			}
			halowErr += "multicast listener: " + le
		}
	}
	s.HaLow.Error = halowErr
	s.HaLow.Fault = halowErr != ""

	st := b.radio.Status()
	s.Meshtastic.Connected = st.Connected
	if st.NodeNum != 0 {
		s.Meshtastic.Node = meshtastic.NodeID(st.NodeNum)
	}
	recent := !b.meshErrAt.IsZero() && now.Sub(b.meshErrAt) <= b.cfg.MeshFaultHold
	switch {
	case !st.Connected && st.LastError != "":
		s.Meshtastic.Error = st.LastError
	case !st.Connected:
		s.Meshtastic.Error = "radio not connected (no config handshake yet)"
	case recent:
		s.Meshtastic.Error = b.meshErr
	}
	s.Meshtastic.Fault = !st.Connected || recent

	s.Forwarding = b.Forwarding()
	s.Forced = !b.lastForced.IsZero() && now.Sub(b.lastForced) <= b.cfg.ForcedWindow
	s.TxCount, s.RxCount = b.txCount, b.rxCount
	s.LastTx, s.LastRx = b.lastTxAt, b.lastRxAt
	s.EUDs = len(b.euds.Targets(now))
	s.QueuedChats = len(b.chats)
	s.QueuedPLIs = len(b.plis)
	return s
}

// preview returns the start of a datagram for logs.
func preview(data []byte) string {
	const n = 60
	if len(data) > n {
		return string(data[:n]) + "…"
	}
	return string(data)
}

// distanceMeters is the great-circle distance between two points.
func distanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}
