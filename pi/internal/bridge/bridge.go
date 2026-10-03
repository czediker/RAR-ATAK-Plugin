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
package bridge

import (
	"errors"
	"log/slog"
	"math"
	"net/netip"
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

// Deliverer sends a CoT event to a local EUD.
type Deliverer interface {
	Deliver(addr netip.Addr, data []byte) error
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
	}
}

type queuedChat struct {
	payload  []byte
	enqueued time.Time
	failures int
	desc     string
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

	lastForced time.Time
	senders    map[string]takconv.Sender
	positions  map[string]position
	rxSeen     map[rxKey]time.Time

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
		plis:      map[string]*queuedPLI{},
		lastPLI:   map[string]sentPLI{},
		senders:   map[string]takconv.Sender{},
		positions: map[string]position{},
		rxSeen:    map[rxKey]time.Time{},
	}
}

// Forwarding reports whether port-6700 traffic currently goes to
// Meshtastic. Until the first neighbor check the answer is yes, so a
// radio that boots out of range is never silent.
func (b *Bridge) Forwarding() bool { return b.link != halow.LinkConnected }

// SetLink records a HaLow monitor report.
func (b *Bridge) SetLink(r halow.Report) {
	if r.Link != b.link && r.Link == halow.LinkConnected {
		b.log.Info("HaLow neighbors present; port 6700 traffic stays on HaLow")
	} else if r.Link != b.link && r.Link == halow.LinkIsolated {
		b.log.Info("no HaLow neighbors; forwarding port 6700 traffic over Meshtastic")
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
		b.log.Debug("ignoring non-CoT datagram", "port", port, "src", src, "err", err)
		return
	}
	uid := ev.UID
	if ev.IsChat() {
		uid = ev.Chat.SenderUID
	}
	b.euds.Learn(src, uid, now)
	if port == PortAlways {
		b.lastForced = now
	}
	if ev.IsPLI() {
		b.senders[ev.UID] = takconv.Sender{Team: ev.Team, Role: ev.Role, Battery: ev.Battery}
	}
	forward := port == PortAlways || b.Forwarding()
	b.log.Debug("from plugin", "port", port, "src", src, "type", ev.Type, "uid", uid, "forward", forward)
	if !forward {
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
			b.log.Warn("chat queue full; dropping oldest", "desc", b.chats[0].desc)
			b.chats = b.chats[1:]
		}
		b.chats = append(b.chats, queuedChat{payload: payload, enqueued: now, desc: ev.Chat.SenderCallsign + " → " + ev.Chat.ToUID})
	case ev.IsPLI():
		payload, err := b.encode(ev, nil)
		if err != nil {
			return
		}
		b.plis[ev.UID] = &queuedPLI{payload: payload, lat: ev.Lat, lon: ev.Lon, port: port, enqueued: now, callsign: ev.Callsign}
	default:
		b.log.Debug("ignoring CoT type", "type", ev.Type)
	}
}

func (b *Bridge) encode(ev *cot.Event, sender *takconv.Sender) ([]byte, error) {
	pkt, err := takconv.FromEvent(ev, sender)
	if err == nil {
		var payload []byte
		if payload, err = takconv.Marshal(pkt); err == nil {
			return payload, nil
		}
	}
	b.log.Warn("cannot convert CoT to TAKPacket", "uid", ev.UID, "type", ev.Type, "err", err)
	return nil, err
}

// Tick expires old state and transmits at most one queued packet.
func (b *Bridge) Tick(now time.Time) {
	b.expire(now)

	if !b.radio.Status().Connected {
		return
	}
	if !b.lastTx.IsZero() && now.Sub(b.lastTx) < b.cfg.TxGap {
		return
	}
	if len(b.chats) > 0 {
		c := &b.chats[0]
		if b.send(c.payload, now, "chat", c.desc) {
			b.chats = b.chats[1:]
		} else if c.failures++; c.failures >= 3 {
			b.log.Warn("giving up on chat after repeated send failures", "desc", c.desc)
			b.chats = b.chats[1:]
		}
		return
	}
	if uid, q := b.nextPLI(now); q != nil {
		if b.send(q.payload, now, "position", q.callsign) {
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
		if !b.pliDue(uid, q, now) {
			continue
		}
		if best == nil || q.enqueued.Before(best.enqueued) {
			bestUID, best = uid, q
		}
	}
	return bestUID, best
}

func (b *Bridge) pliDue(uid string, q *queuedPLI, now time.Time) bool {
	last, ok := b.lastPLI[uid]
	if !ok {
		return true
	}
	elapsed := now.Sub(last.at)
	if elapsed >= b.cfg.PLIInterval {
		return true
	}
	return elapsed >= b.cfg.PLIMinInterval && distanceMeters(last.lat, last.lon, q.lat, q.lon) >= b.cfg.PLIMoveMeters
}

func (b *Bridge) send(payload []byte, now time.Time, kind, desc string) bool {
	b.lastTx = now
	id, err := b.radio.SendData(meshpb.PortNum_ATAK_PLUGIN, payload)
	if err != nil {
		if !errors.Is(err, meshtastic.ErrNotConnected) {
			b.log.Warn("meshtastic send failed", "kind", kind, "err", err)
		}
		return false
	}
	b.txCount++
	b.lastTxAt = now
	b.log.Info("sent over meshtastic", "kind", kind, "who", desc, "bytes", len(payload), "id", id)
	return true
}

func (b *Bridge) expire(now time.Time) {
	for len(b.chats) > 0 && now.Sub(b.chats[0].enqueued) > b.cfg.ChatMaxAge {
		b.log.Warn("dropping chat that could not be sent in time", "desc", b.chats[0].desc)
		b.chats = b.chats[1:]
	}
	for uid, q := range b.plis {
		// Position reports that only needed Meshtastic because HaLow was
		// down are stale once HaLow is back.
		if q.port == PortAuto && !b.Forwarding() {
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

// HandleRadio processes a packet received from the Meshtastic radio.
func (b *Bridge) HandleRadio(p *meshpb.MeshPacket, now time.Time) {
	d := p.GetDecoded()
	if d == nil || d.GetPortnum() != meshpb.PortNum_ATAK_PLUGIN {
		return
	}
	key := rxKey{p.GetFrom(), p.GetId()}
	if _, dup := b.rxSeen[key]; dup {
		return
	}
	pkt, err := takconv.Decode(d.GetPayload())
	if err != nil {
		b.log.Warn("bad TAKPacket", "from", meshtastic.NodeID(p.GetFrom()), "err", err)
		return
	}
	if pkt.GetIsCompressed() {
		// Older firmware/plugins compress strings with unishox2. Firmware
		// that does this also hands clients a decompressed copy with the
		// same packet ID, so skip without marking the ID as seen.
		b.log.Debug("skipping compressed TAKPacket", "from", meshtastic.NodeID(p.GetFrom()), "id", p.GetId())
		return
	}
	b.rxSeen[key] = now

	uid := takconv.SenderUID(pkt)
	if uid == "" || b.euds.IsLocalUID(uid, now) {
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
		b.log.Debug("dropping meshtastic copy of traffic already heard over HaLow", "uid", uid)
		return
	}
	if chat := pkt.GetChat(); chat != nil {
		to := chat.GetTo()
		if to != "" && to != cot.AllChatRooms {
			if local := b.euds.LocalUIDs(now); len(local) > 0 && !contains(local, to) {
				b.log.Debug("direct message for another user", "to", to)
				return
			}
		}
	}

	x, err := takconv.ToCoT(pkt, takconv.CoTOptions{
		Now:       now,
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
		b.log.Debug("unsupported TAKPacket", "from", meshtastic.NodeID(p.GetFrom()), "err", err)
		return
	}
	targets := b.euds.Targets(now)
	if len(targets) == 0 {
		b.log.Info("received over meshtastic but no local EUD known yet", "uid", uid)
		return
	}
	kind := "position"
	if pkt.GetChat() != nil {
		kind = "chat"
	}
	for _, a := range targets {
		if err := b.deliver.Deliver(a, x); err != nil {
			b.log.Warn("delivery to EUD failed", "addr", a, "err", err)
		}
	}
	b.log.Info("received over meshtastic", "kind", kind, "callsign", pkt.GetContact().GetCallsign(), "from", meshtastic.NodeID(p.GetFrom()), "euds", len(targets))
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
	s.HaLow.Error = b.linkErr
	st := b.radio.Status()
	s.Meshtastic.Connected = st.Connected
	if st.NodeNum != 0 {
		s.Meshtastic.Node = meshtastic.NodeID(st.NodeNum)
	}
	s.Meshtastic.Error = st.LastError
	s.Forwarding = b.Forwarding()
	s.Forced = !b.lastForced.IsZero() && now.Sub(b.lastForced) <= b.cfg.ForcedWindow
	s.TxCount, s.RxCount = b.txCount, b.rxCount
	s.LastTx, s.LastRx = b.lastTxAt, b.lastRxAt
	s.EUDs = len(b.euds.Targets(now))
	s.QueuedChats = len(b.chats)
	s.QueuedPLIs = len(b.plis)
	return s
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
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
