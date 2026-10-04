package bridge

import (
	"bytes"
	"io"
	"log/slog"
	"math"
	"net/netip"
	"strings"
	"testing"
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

type fakeRadio struct {
	connected bool
	sent      []*meshpb.TAKPacket
	ports     []meshpb.PortNum
}

func (f *fakeRadio) SendData(port meshpb.PortNum, payload []byte) (uint32, error) {
	if !f.connected {
		return 0, meshtastic.ErrNotConnected
	}
	pkt, err := takconv.Decode(payload)
	if err != nil {
		return 0, err
	}
	f.sent = append(f.sent, pkt)
	f.ports = append(f.ports, port)
	return uint32(len(f.sent)), nil
}

func (f *fakeRadio) Status() meshtastic.Status {
	return meshtastic.Status{Connected: f.connected, NodeNum: 0x0000beef}
}

type delivery struct {
	addr netip.Addr
	ev   *cot.Event
}

type fakeDeliverer struct{ got []delivery }

func (f *fakeDeliverer) Deliver(addr netip.Addr, data []byte) (string, error) {
	ev, err := cot.Parse(data)
	if err != nil {
		panic(err)
	}
	f.got = append(f.got, delivery{addr, ev})
	return netip.AddrPortFrom(addr, 4242).String(), nil
}

var (
	t0    = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	phone = netip.MustParseAddr("10.41.113.200")
)

const selfUID = "ANDROID-self"

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

type harness struct {
	b     *Bridge
	radio *fakeRadio
	out   *fakeDeliverer
	euds  *eud.Registry
	seen  *halowseen.Tracker
	logs  *bytes.Buffer
}

func newHarness(link halow.Link) *harness {
	h := &harness{radio: &fakeRadio{connected: true}, out: &fakeDeliverer{}, euds: eud.NewRegistry(15 * time.Minute), seen: halowseen.NewTracker(time.Minute), logs: &bytes.Buffer{}}
	h.b = New(DefaultConfig(), h.radio, h.out, h.euds, h.seen, slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if link != halow.LinkUnknown {
		h.b.SetLink(halow.Report{Link: link, Neighbors: 1})
	}
	return h
}

func pli(lat, lon float64) []byte {
	return cot.BuildPLI(cot.PLI{UID: selfUID, Callsign: "ALPHA", Lat: lat, Lon: lon, HAE: 10, Team: "Cyan", Role: "Team Lead", Battery: 77, Speed: math.NaN(), Course: math.NaN(), Time: t0, Stale: time.Minute})
}

func chat(text string) []byte {
	return cot.BuildGeoChat(cot.GeoChat{MessageID: text, SenderUID: selfUID, SenderCallsign: "ALPHA", ToUID: cot.AllChatRooms, Text: text, Time: t0, Stale: time.Hour})
}

func TestAutoPortFollowsHaLowState(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAuto, phone, chat("not sent"), at(0))
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.b.Tick(at(1))
	if len(h.radio.sent) != 0 {
		t.Fatalf("connected: sent %d packets", len(h.radio.sent))
	}
	if !h.euds.IsLocalUID(selfUID, at(1)) || len(h.euds.Targets(at(1))) != 1 {
		t.Error("EUD should be learned even when not forwarding")
	}

	h.b.SetLink(halow.Report{Link: halow.LinkIsolated})
	h.b.HandleEUD(PortAuto, phone, chat("sent"), at(2))
	h.b.Tick(at(2))
	if len(h.radio.sent) != 1 || h.radio.sent[0].GetChat().GetMessage() != "sent" || h.radio.ports[0] != meshpb.PortNum_ATAK_PLUGIN {
		t.Fatalf("isolated: sent %v", h.radio.sent)
	}
	// The chat carries the sender's group/status learned from its PLI.
	if g := h.radio.sent[0].GetGroup(); g.GetTeam() != meshpb.Team_Cyan || g.GetRole() != meshpb.MemberRole_TeamLead || h.radio.sent[0].GetStatus().GetBattery() != 77 {
		t.Errorf("chat sender info: %v", h.radio.sent[0])
	}
}

func TestUnknownLinkForwards(t *testing.T) {
	h := newHarness(halow.LinkUnknown)
	if !h.b.Forwarding() {
		t.Fatal("unknown link should forward")
	}
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.b.Tick(at(0))
	if len(h.radio.sent) != 1 {
		t.Fatalf("sent %d", len(h.radio.sent))
	}
}

func TestAlwaysPortForwardsAndSetsForced(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAlways, phone, pli(1, 2), at(0))
	h.b.Tick(at(0))
	if len(h.radio.sent) != 1 || h.radio.sent[0].GetPli() == nil {
		t.Fatalf("sent %v", h.radio.sent)
	}
	if s := h.b.Snapshot(at(10)); !s.Forced || s.Forwarding || s.HaLow.State != state.HaLowConnected {
		t.Errorf("snapshot = %+v", s)
	}
	if s := h.b.Snapshot(at(91)); s.Forced {
		t.Error("forced should time out")
	}
}

func TestChatPriorityAndTxGap(t *testing.T) {
	h := newHarness(halow.LinkIsolated)
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.b.HandleEUD(PortAuto, phone, chat("one"), at(0))
	h.b.HandleEUD(PortAuto, phone, chat("two"), at(0))
	h.b.Tick(at(0))
	h.b.Tick(at(1)) // within TxGap: nothing
	h.b.Tick(at(3))
	h.b.Tick(at(6))
	if len(h.radio.sent) != 3 {
		t.Fatalf("sent %d", len(h.radio.sent))
	}
	if h.radio.sent[0].GetChat().GetMessage() != "one" || h.radio.sent[1].GetChat().GetMessage() != "two" || h.radio.sent[2].GetPli() == nil {
		t.Errorf("order wrong: %v", h.radio.sent)
	}
}

func TestPLIRateLimit(t *testing.T) {
	h := newHarness(halow.LinkIsolated)
	tick := func(sec float64) { h.b.Tick(at(sec)) }

	h.b.HandleEUD(PortAuto, phone, pli(10, 10), at(0))
	tick(0)
	if len(h.radio.sent) != 1 {
		t.Fatal("first PLI should go immediately")
	}
	// Stationary updates are held until PLIInterval, and the latest wins.
	h.b.HandleEUD(PortAuto, phone, pli(10, 10.00001), at(5))
	h.b.HandleEUD(PortAuto, phone, pli(10, 10.00002), at(30))
	tick(30)
	tick(59)
	if len(h.radio.sent) != 1 {
		t.Fatalf("held PLI sent early: %d", len(h.radio.sent))
	}
	tick(60)
	if len(h.radio.sent) != 2 || h.radio.sent[1].GetPli().GetLongitudeI() != 100000200 {
		t.Fatalf("expected latest PLI at 60s, got %v", h.radio.sent)
	}
	// Moving ≥50 m allows a report after PLIMinInterval.
	h.b.HandleEUD(PortAuto, phone, pli(10.001, 10.00002), at(65)) // ~111 m north
	tick(70)
	if len(h.radio.sent) != 2 {
		t.Fatal("moving PLI sent before min interval")
	}
	tick(75)
	if len(h.radio.sent) != 3 {
		t.Fatal("moving PLI not sent after min interval")
	}
}

func TestAutoPLIDroppedWhenHaLowReturns(t *testing.T) {
	h := newHarness(halow.LinkIsolated)
	h.radio.connected = false
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.b.HandleEUD(PortAuto, phone, chat("queued"), at(0))
	h.b.Tick(at(1))
	if s := h.b.Snapshot(at(1)); s.QueuedPLIs != 1 || s.QueuedChats != 1 {
		t.Fatalf("queues = %d/%d", s.QueuedPLIs, s.QueuedChats)
	}
	h.b.SetLink(halow.Report{Link: halow.LinkConnected, Neighbors: 2})
	h.radio.connected = true
	h.b.Tick(at(2))
	h.b.Tick(at(10))
	if len(h.radio.sent) != 1 || h.radio.sent[0].GetChat() == nil {
		t.Fatalf("expected only the queued chat, got %v", h.radio.sent)
	}
}

func TestChatExpiresWhileRadioDown(t *testing.T) {
	h := newHarness(halow.LinkIsolated)
	h.radio.connected = false
	h.b.HandleEUD(PortAuto, phone, chat("old"), at(0))
	h.b.Tick(at(601))
	h.radio.connected = true
	h.b.Tick(at(602))
	if len(h.radio.sent) != 0 {
		t.Fatalf("expired chat was sent")
	}
}

func TestChatQueueBounded(t *testing.T) {
	h := newHarness(halow.LinkIsolated)
	h.radio.connected = false
	for i := 0; i < 40; i++ {
		h.b.HandleEUD(PortAuto, phone, chat(string(rune('A'+i))), at(0))
	}
	if s := h.b.Snapshot(at(0)); s.QueuedChats != 32 {
		t.Fatalf("queued = %d", s.QueuedChats)
	}
}

func TestIgnoresGarbageAndOtherTypes(t *testing.T) {
	h := newHarness(halow.LinkIsolated)
	h.b.HandleEUD(PortAuto, phone, []byte("hello"), at(0))
	h.b.HandleEUD(PortAuto, phone, []byte(`<event version="2.0" uid="m1" type="b-m-p-s-p-i"><point lat="1" lon="2"/><detail/></event>`), at(0))
	h.b.Tick(at(0))
	if len(h.radio.sent) != 0 {
		t.Fatal("sent unexpected packet")
	}
}

// radioPacket builds a received MeshPacket carrying a TAKPacket.
func radioPacket(from, id uint32, pkt *meshpb.TAKPacket) *meshpb.MeshPacket {
	payload, err := takconv.Marshal(pkt)
	if err != nil {
		panic(err)
	}
	return &meshpb.MeshPacket{From: from, Id: id, PayloadVariant: &meshpb.MeshPacket_Decoded{Decoded: &meshpb.Data{Portnum: meshpb.PortNum_ATAK_PLUGIN, Payload: payload}}}
}

func remotePLI(uid string) *meshpb.TAKPacket {
	return &meshpb.TAKPacket{
		Contact:        &meshpb.Contact{Callsign: "BRAVO", DeviceCallsign: uid},
		PayloadVariant: &meshpb.TAKPacket_Pli{Pli: &meshpb.PLI{LatitudeI: 123456789, LongitudeI: -987654321, Altitude: 40}},
	}
}

func remoteChat(uid, to, text string) *meshpb.TAKPacket {
	c := &meshpb.GeoChat{Message: text, To: &to}
	return &meshpb.TAKPacket{Contact: &meshpb.Contact{Callsign: "BRAVO", DeviceCallsign: uid}, PayloadVariant: &meshpb.TAKPacket_Chat{Chat: c}}
}

func TestReceiveDeliversToLocalEUD(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0)) // learn the phone

	h.b.HandleRadio(radioPacket(0x1111, 1, remotePLI("ANDROID-bravo")), at(1))
	h.b.HandleRadio(radioPacket(0x1111, 1, remotePLI("ANDROID-bravo")), at(2)) // duplicate
	h.b.HandleRadio(radioPacket(0x1111, 2, remoteChat("ANDROID-bravo", cot.AllChatRooms, "hi team")), at(3))
	if len(h.out.got) != 2 {
		t.Fatalf("deliveries = %d", len(h.out.got))
	}
	p := h.out.got[0]
	if p.addr != phone || !p.ev.IsPLI() || p.ev.UID != "ANDROID-bravo" || p.ev.Callsign != "BRAVO" || math.Abs(p.ev.Lat-12.3456789) > 1e-7 {
		t.Errorf("pli delivery = %+v", p.ev)
	}
	c := h.out.got[1]
	if !c.ev.IsChat() || c.ev.Chat.Text != "hi team" || c.ev.Chat.SenderUID != "ANDROID-bravo" {
		t.Errorf("chat delivery = %+v", c.ev.Chat)
	}
	// The chat is placed at the sender's last known position.
	if math.Abs(c.ev.Lat-12.3456789) > 1e-7 || c.ev.HAE != 40 {
		t.Errorf("chat position = %v %v", c.ev.Lat, c.ev.HAE)
	}
	if s := h.b.Snapshot(at(3)); s.RxCount != 2 || s.LastRx != at(3) {
		t.Errorf("rx stats = %d %v", s.RxCount, s.LastRx)
	}
}

func TestReceiveFilters(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))

	// Wrong port, encrypted, compressed and own-UID packets are ignored.
	h.b.HandleRadio(&meshpb.MeshPacket{From: 1, Id: 1, PayloadVariant: &meshpb.MeshPacket_Decoded{Decoded: &meshpb.Data{Portnum: meshpb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hi")}}}, at(1))
	h.b.HandleRadio(&meshpb.MeshPacket{From: 1, Id: 2, PayloadVariant: &meshpb.MeshPacket_Encrypted{Encrypted: []byte{1, 2}}}, at(1))
	comp := remotePLI("ANDROID-bravo")
	comp.IsCompressed = true
	h.b.HandleRadio(radioPacket(1, 3, comp), at(1))
	h.b.HandleRadio(radioPacket(1, 4, remotePLI(selfUID)), at(1))
	// Direct message for somebody else.
	h.b.HandleRadio(radioPacket(1, 5, remoteChat("ANDROID-bravo", "ANDROID-charlie", "not for you")), at(1))
	if len(h.out.got) != 0 {
		t.Fatalf("unexpected deliveries: %+v", h.out.got)
	}
	// Direct message for our user is delivered.
	h.b.HandleRadio(radioPacket(1, 6, remoteChat("ANDROID-bravo", selfUID, "for you")), at(1))
	if len(h.out.got) != 1 || h.out.got[0].ev.Chat.ToUID != selfUID {
		t.Fatalf("direct message not delivered: %+v", h.out.got)
	}
	// The compressed packet's ID was not marked seen: an uncompressed copy
	// with the same ID is still accepted.
	h.b.HandleRadio(radioPacket(1, 3, remotePLI("ANDROID-bravo")), at(2))
	if len(h.out.got) != 2 {
		t.Fatalf("uncompressed copy not delivered")
	}
}

func TestReceiveSuppressesUsersHeardOverHaLow(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.seen.Mark("ANDROID-bravo", at(0))
	h.b.HandleRadio(radioPacket(1, 1, remoteChat("ANDROID-bravo", cot.AllChatRooms, "dup")), at(10))
	if len(h.out.got) != 0 {
		t.Fatal("HaLow duplicate delivered")
	}
	// Once the user has not been heard over HaLow for the window, deliver.
	h.b.HandleRadio(radioPacket(1, 2, remoteChat("ANDROID-bravo", cot.AllChatRooms, "isolated now")), at(120))
	if len(h.out.got) != 1 {
		t.Fatal("isolated user's chat not delivered")
	}
}

func TestReceiveWithoutKnownEUD(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	h.b.HandleRadio(radioPacket(1, 1, remotePLI("ANDROID-bravo")), at(0))
	if len(h.out.got) != 0 {
		t.Fatal("delivered without a target")
	}
	// Leases provide targets even before the plugin has sent anything.
	h.euds.SetLeases([]netip.Addr{phone})
	h.b.HandleRadio(radioPacket(1, 2, remotePLI("ANDROID-bravo")), at(1))
	if len(h.out.got) != 1 {
		t.Fatal("lease target not used")
	}
}

func TestSnapshot(t *testing.T) {
	h := newHarness(halow.LinkIsolated)
	h.b.SetLink(halow.Report{Link: halow.LinkIsolated, Err: io.EOF})
	s := h.b.Snapshot(at(0))
	if s.HaLow.State != state.HaLowIsolated || s.HaLow.Error == "" || !s.Forwarding || !s.Meshtastic.Connected || s.Meshtastic.Node != "!0000beef" {
		t.Errorf("snapshot = %+v", s)
	}
	if New(DefaultConfig(), h.radio, h.out, h.euds, nil, nil).Snapshot(at(0)).HaLow.State != state.HaLowUnknown {
		t.Error("initial state should be unknown")
	}
}

func TestDistance(t *testing.T) {
	// One degree of latitude ≈ 111.2 km.
	if d := distanceMeters(0, 0, 1, 0); math.Abs(d-111195) > 100 {
		t.Errorf("distance = %v", d)
	}
	if d := distanceMeters(38.9, -77, 38.9, -77); d != 0 {
		t.Errorf("zero distance = %v", d)
	}
}

func (h *harness) requireLog(t *testing.T, substrs ...string) {
	t.Helper()
	for _, sub := range substrs {
		if !strings.Contains(h.logs.String(), sub) {
			t.Errorf("log missing %q", sub)
		}
	}
}

func TestRadioAcks(t *testing.T) {
	h := newHarness(halow.LinkIsolated)
	h.b.HandleEUD(PortAuto, phone, chat("one"), at(0))
	h.b.HandleEUD(PortAuto, phone, chat("two"), at(0))
	h.b.HandleEUD(PortAuto, phone, chat("three"), at(0))
	h.b.Tick(at(0))
	h.b.HandleAck(&meshpb.QueueStatus{MeshPacketId: 1, Free: 15, Maxlen: 16}, at(0.2))
	if s := h.b.Snapshot(at(1)); s.Meshtastic.Fault {
		t.Fatalf("accepted packet should not fault: %+v", s.Meshtastic)
	}
	h.requireLog(t, "Meshtastic radio accepted the message for transmission")

	h.b.Tick(at(3))
	h.b.HandleAck(&meshpb.QueueStatus{MeshPacketId: 2, Res: 32}, at(3.1))
	s := h.b.Snapshot(at(4))
	if !s.Meshtastic.Fault || !strings.Contains(s.Meshtastic.Error, "rejected") {
		t.Fatalf("rejection should fault: %+v", s.Meshtastic)
	}
	h.requireLog(t, "Meshtastic radio REJECTED the message")
	if s := h.b.Snapshot(at(3.1 + 31)); s.Meshtastic.Fault {
		t.Error("fault should clear after MeshFaultHold")
	}

	// Third packet never confirmed.
	h.b.Tick(at(40))
	h.b.Tick(at(51))
	if s := h.b.Snapshot(at(51)); !s.Meshtastic.Fault || !strings.Contains(s.Meshtastic.Error, "did not confirm") {
		t.Fatalf("missing ack should fault: %+v", s.Meshtastic)
	}
	h.requireLog(t, "Meshtastic radio did not confirm the message")
}

func TestFaultFlags(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	if s := h.b.Snapshot(at(0)); s.HaLow.Fault || s.Meshtastic.Fault {
		t.Fatalf("healthy: %+v", s)
	}
	h.b.SetLink(halow.Report{Link: halow.LinkIsolated, Err: io.ErrUnexpectedEOF})
	if s := h.b.Snapshot(at(0)); !s.HaLow.Fault || s.HaLow.Error == "" {
		t.Errorf("batctl error should set the HaLow fault: %+v", s.HaLow)
	}
	h.radio.connected = false
	if s := h.b.Snapshot(at(0)); !s.Meshtastic.Fault || s.Meshtastic.Error == "" {
		t.Errorf("disconnected radio should fault: %+v", s.Meshtastic)
	}
}

func TestDebugTrail(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.requireLog(t, "plugin message received", "uid=ANDROID-self", "callsign=ALPHA", "team=Cyan", "role=\"Team Lead\"",
		"cot_bytes=", "not sent to Meshtastic: HaLow has neighbors", "local EUD learned")

	h.b.HandleEUD(PortAlways, phone, chat("outgoing secret"), at(0.5))
	h.b.HandleEUD(PortAlways, phone, pli(1, 2), at(1))
	h.b.HandleEUD(PortAlways, phone, pli(1, 2.001), at(1.5))
	h.requireLog(t, "converted to TAKPacket", "takpacket_bytes=", "position queued for Meshtastic", "newer position replaces an unsent queued one")
	h.b.Tick(at(2))
	h.requireLog(t, "passed to Meshtastic radio")

	h.b.HandleRadio(radioPacket(0x1111, 9, remoteChat("ANDROID-bravo", cot.AllChatRooms, "hello")), at(3))
	h.b.HandleRadio(radioPacket(0x1111, 9, remoteChat("ANDROID-bravo", cot.AllChatRooms, "hello")), at(4))
	h.requireLog(t, "ATAK data received from Meshtastic", "payload_bytes=", "text_bytes=5", "sent to ATAK", "dest=10.41.113.200:4242", "DUPLICATE Meshtastic packet")

	h.seen.Mark("ANDROID-charlie", at(5))
	h.b.HandleRadio(radioPacket(0x2222, 1, remotePLI("ANDROID-charlie")), at(6))
	h.requireLog(t, "DEDUPE: sender is reachable over HaLow")

	h.b.HandleRadio(&meshpb.MeshPacket{From: 3, Id: 1, PayloadVariant: &meshpb.MeshPacket_Decoded{Decoded: &meshpb.Data{Portnum: meshpb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("7")}}}, at(7))
	h.requireLog(t, "not ATAK traffic; ignored", "portnum=TEXT_MESSAGE_APP", "payload_bytes=1")

	// Message text is never logged, only its size.
	if strings.Contains(h.logs.String(), "text=") {
		t.Errorf("message text logged:\n%s", h.logs.String())
	}
}

// The Pi's clock is often wrong (no RTC, no internet). CoT delivered to ATAK
// must carry the EUD's time, learned from the plugin's own traffic: ATAK
// orders chat and judges staleness by it.
func TestDeliveriesUseEUDClock(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	ahead := 3 * time.Hour
	eudPLI := cot.BuildPLI(cot.PLI{UID: selfUID, Callsign: "ALPHA", Lat: 1, Lon: 2, HAE: 10, Battery: -1, Speed: math.NaN(), Course: math.NaN(), Time: t0.Add(ahead), Stale: time.Minute})
	h.b.HandleEUD(PortAuto, phone, eudPLI, at(0))
	h.requireLog(t, "clock differs from the EUD's", "eud_ahead_by=3h0m0s")

	h.b.HandleRadio(radioPacket(0x1111, 1, remotePLI("ANDROID-bravo")), at(5))
	h.b.HandleRadio(radioPacket(0x1111, 2, remoteChat("ANDROID-bravo", cot.AllChatRooms, "hi")), at(6))
	if len(h.out.got) != 2 {
		t.Fatalf("deliveries = %d", len(h.out.got))
	}
	if got, want := h.out.got[0].ev.Time, at(5).Add(ahead); !got.Equal(want) {
		t.Errorf("pli time = %v, want %v", got, want)
	}
	if got, want := h.out.got[1].ev.Time, at(6).Add(ahead); !got.Equal(want) {
		t.Errorf("chat time = %v, want %v", got, want)
	}
	if !h.out.got[0].ev.Stale.After(at(6).Add(ahead)) {
		t.Errorf("pli already stale on the EUD's clock: %v", h.out.got[0].ev.Stale)
	}
}

// Chat from the Meshtastic ATAK plugin carries "uid|messageId" as its sender.
func TestReceiveChatFromMeshtasticPlugin(t *testing.T) {
	h := newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.b.HandleRadio(radioPacket(0x2222, 1, remotePLI("ANDROID-bravo")), at(1))
	h.b.HandleRadio(radioPacket(0x2222, 2, remoteChat("ANDROID-bravo|0d1e2f30-aaaa-4bbb-8ccc-dddddddddddd", cot.AllChatRooms, "from stock")), at(2))
	if len(h.out.got) != 2 {
		t.Fatalf("deliveries = %d", len(h.out.got))
	}
	c := h.out.got[1].ev
	if c.Chat.SenderUID != "ANDROID-bravo" || c.Chat.MessageID != "0d1e2f30-aaaa-4bbb-8ccc-dddddddddddd" {
		t.Errorf("chat = %+v", c.Chat)
	}
	// Linked to the sender's marker: placed at its position.
	if math.Abs(c.Lat-12.3456789) > 1e-7 {
		t.Errorf("chat not placed at the sender: %v", c.Lat)
	}
}

func compressedPacket(from, id uint32, pkt *meshpb.TAKPacket) *meshpb.MeshPacket {
	pkt.IsCompressed = true
	return radioPacket(from, id, pkt)
}

func TestCompressedPacketsWithoutDecompressedCopy(t *testing.T) {
	// Role TAK: the decompressed copy follows, nothing to report.
	h := newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.b.HandleRadio(compressedPacket(0x3333, 1, remotePLI("ANDROID-c")), at(1))
	h.b.HandleRadio(radioPacket(0x3333, 1, remotePLI("ANDROID-c")), at(1.1))
	h.b.Tick(at(10))
	if len(h.out.got) != 1 || strings.Contains(h.logs.String(), "did not decompress") {
		t.Fatalf("deliveries = %d, logs:\n%s", len(h.out.got), h.logs)
	}

	// Role not TAK: only the compressed packet arrives.
	h = newHarness(halow.LinkConnected)
	h.b.HandleEUD(PortAuto, phone, pli(1, 2), at(0))
	h.b.HandleRadio(compressedPacket(0x3333, 1, remotePLI("ANDROID-c")), at(1))
	h.b.HandleRadio(compressedPacket(0x3333, 2, remoteChat("ANDROID-c", cot.AllChatRooms, "x")), at(1.5))
	h.b.Tick(at(2)) // too early
	if strings.Contains(h.logs.String(), "did not decompress") {
		t.Fatal("warned before the decompressed copy could arrive")
	}
	h.b.Tick(at(5))
	h.requireLog(t, "did not decompress", "device role to TAK", "packets=2")
	if s := h.b.Snapshot(at(5)); !s.Meshtastic.Fault || !strings.Contains(s.Meshtastic.Error, "role to TAK") {
		t.Errorf("snapshot = %+v", s.Meshtastic)
	}
	if len(h.out.got) != 0 {
		t.Errorf("delivered %d undecodable packets", len(h.out.got))
	}
}
