package takconv

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/czediker/rar-atak-plugin/pi/internal/cot"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
)

func pliEvent() *cot.Event {
	return &cot.Event{
		UID: "ANDROID-589520ccfcd20f01", Type: "a-f-G-U-C", Callsign: "ALPHA",
		Lat: 38.8977123, Lon: -77.0365456, HAE: 25.6,
		Team: "Dark Green", Role: "Forward Observer", Battery: 88, Speed: 1.6, Course: 359.7,
	}
}

func chatEvent(to, room, text string) *cot.Event {
	return &cot.Event{
		UID: "GeoChat.ANDROID-aaa." + to + ".m1", Type: cot.ChatType,
		Chat: &cot.Chat{SenderUID: "ANDROID-aaa", SenderCallsign: "ALPHA", ToUID: to, Chatroom: room, Text: text, MessageID: "m1"},
	}
}

func TestPLIRoundTrip(t *testing.T) {
	pkt, err := FromEvent(pliEvent(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxPayload {
		t.Fatalf("payload %d bytes", len(b))
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	pli := got.GetPli()
	if pli.GetLatitudeI() != 388977123 || pli.GetLongitudeI() != -770365456 {
		t.Errorf("lat/lon = %d/%d", pli.GetLatitudeI(), pli.GetLongitudeI())
	}
	if pli.GetAltitude() != 26 || pli.GetSpeed() != 2 || pli.GetCourse() != 0 {
		t.Errorf("alt/speed/course = %d/%d/%d", pli.GetAltitude(), pli.GetSpeed(), pli.GetCourse())
	}
	if got.GetGroup().GetTeam() != meshpb.Team_Dark_Green || got.GetGroup().GetRole() != meshpb.MemberRole_ForwardObserver {
		t.Errorf("group = %v", got.GetGroup())
	}
	if got.GetStatus().GetBattery() != 88 || got.GetIsCompressed() {
		t.Errorf("status = %v compressed=%v", got.GetStatus(), got.GetIsCompressed())
	}

	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	x, err := ToCoT(got, CoTOptions{Now: now, PLIStale: 6 * time.Minute, Via: cot.Via{Transport: "meshtastic", Node: "!1"}})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := cot.Parse(x)
	if err != nil {
		t.Fatalf("%v\n%s", err, x)
	}
	if ev.UID != "ANDROID-589520ccfcd20f01" || ev.Callsign != "ALPHA" || ev.Team != "Dark Green" || ev.Role != "Forward Observer" {
		t.Errorf("event = %+v", ev)
	}
	if math.Abs(ev.Lat-38.8977123) > 1e-7 || math.Abs(ev.Lon+77.0365456) > 1e-7 || ev.HAE != 26 || ev.Battery != 88 {
		t.Errorf("event position/battery = %v %v %v %d", ev.Lat, ev.Lon, ev.HAE, ev.Battery)
	}
	if !ev.Stale.Equal(now.Add(6 * time.Minute)) {
		t.Errorf("stale = %v", ev.Stale)
	}
}

func TestPLIUnknownAltitudeAndMissingGroup(t *testing.T) {
	ev := &cot.Event{UID: "u", Type: "a-f-G", Lat: 1, Lon: 2, HAE: cot.Unknown, Battery: -1, Speed: math.NaN(), Course: math.NaN()}
	pkt, _ := FromEvent(ev, nil)
	if pkt.GetPli().GetAltitude() != 0 || pkt.Group != nil || pkt.Status != nil {
		t.Errorf("pkt = %v", pkt)
	}
	x, err := ToCoT(pkt, CoTOptions{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	back, _ := cot.Parse(x)
	if back.HAE != cot.Unknown {
		t.Errorf("altitude 0 should render as unknown, got %v", back.HAE)
	}
}

func TestBroadcastChatRoundTrip(t *testing.T) {
	pkt, err := FromEvent(chatEvent(cot.AllChatRooms, cot.AllChatRooms, "hello all"), &Sender{Team: "Red", Role: "Medic", Battery: 40})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(pkt)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Decode(b)
	if got.GetChat().GetTo() != cot.AllChatRooms || got.GetChat().ToCallsign != nil {
		t.Errorf("chat = %v", got.GetChat())
	}
	if got.GetGroup().GetTeam() != meshpb.Team_Red || got.GetStatus().GetBattery() != 40 {
		t.Errorf("sender info missing: %v", got)
	}
	opts := CoTOptions{
		Now: time.Now(), ChatStale: time.Hour, FromNode: 0x1234, PacketID: 99,
		SenderPos: func(uid string) (Position, bool) { return Position{Lat: 10, Lon: 20, HAE: 30}, uid == "ANDROID-aaa" },
	}
	x, err := ToCoT(got, opts)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := cot.Parse(x)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.IsChat() || !ev.Chat.IsBroadcast() || ev.Chat.Text != "hello all" || ev.Chat.SenderUID != "ANDROID-aaa" {
		t.Errorf("chat = %+v", ev.Chat)
	}
	if ev.Lat != 10 || ev.Lon != 20 || ev.HAE != 30 {
		t.Errorf("chat position = %v %v %v", ev.Lat, ev.Lon, ev.HAE)
	}
	// Same packet identity → same message ID, so ATAK de-duplicates.
	x2, _ := ToCoT(got, opts)
	ev2, _ := cot.Parse(x2)
	if ev.Chat.MessageID != ev2.Chat.MessageID || ev.UID != ev2.UID {
		t.Errorf("message id not stable: %s vs %s", ev.Chat.MessageID, ev2.Chat.MessageID)
	}
	opts.PacketID = 100
	x3, _ := ToCoT(got, opts)
	ev3, _ := cot.Parse(x3)
	if ev3.Chat.MessageID == ev.Chat.MessageID {
		t.Error("different packet should get a different message id")
	}
}

func TestDirectChatRoundTrip(t *testing.T) {
	pkt, _ := FromEvent(chatEvent("ANDROID-bbb", "BRAVO", "just you"), nil)
	b, err := Marshal(pkt)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Decode(b)
	if got.GetChat().GetTo() != "ANDROID-bbb" || got.GetChat().GetToCallsign() != "BRAVO" {
		t.Errorf("chat = %v", got.GetChat())
	}
	x, _ := ToCoT(got, CoTOptions{Now: time.Now(), ChatStale: time.Hour})
	ev, _ := cot.Parse(x)
	if ev.Chat.IsBroadcast() || ev.Chat.ToUID != "ANDROID-bbb" || ev.Chat.Chatroom != "BRAVO" {
		t.Errorf("chat = %+v", ev.Chat)
	}
}

func TestMarshalTruncatesLongChat(t *testing.T) {
	long := strings.Repeat("é", 300) // 600 bytes of 2-byte runes
	pkt, _ := FromEvent(chatEvent("ANDROID-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "BRAVO-LONG-CALLSIGN", long), &Sender{Team: "Cyan", Role: "HQ", Battery: 1})
	b, err := Marshal(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxPayload {
		t.Fatalf("payload %d bytes", len(b))
	}
	got, _ := Decode(b)
	msg := got.GetChat().GetMessage()
	if !utf8.ValidString(msg) || len(msg) == 0 || len(msg) > maxChatText {
		t.Errorf("message len %d valid=%v", len(msg), utf8.ValidString(msg))
	}
}

func TestMarshalTooLarge(t *testing.T) {
	pkt := &meshpb.TAKPacket{
		Contact:        &meshpb.Contact{Callsign: strings.Repeat("c", 150), DeviceCallsign: strings.Repeat("u", 150)},
		PayloadVariant: &meshpb.TAKPacket_Pli{Pli: &meshpb.PLI{}},
	}
	if _, err := Marshal(pkt); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestUnsupported(t *testing.T) {
	if _, err := FromEvent(&cot.Event{UID: "x", Type: "b-m-p-s-p-i"}, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
	detail := &meshpb.TAKPacket{Contact: &meshpb.Contact{DeviceCallsign: "u"}, PayloadVariant: &meshpb.TAKPacket_Detail{Detail: []byte("<x/>")}}
	if _, err := ToCoT(detail, CoTOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("detail err = %v", err)
	}
	receipt := &meshpb.TAKPacket{Contact: &meshpb.Contact{DeviceCallsign: "u"}, PayloadVariant: &meshpb.TAKPacket_Chat{Chat: &meshpb.GeoChat{ReceiptType: meshpb.GeoChat_ReceiptType_Read}}}
	if _, err := ToCoT(receipt, CoTOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("receipt err = %v", err)
	}
	if _, err := ToCoT(&meshpb.TAKPacket{PayloadVariant: &meshpb.TAKPacket_Pli{Pli: &meshpb.PLI{}}}, CoTOptions{}); err == nil {
		t.Error("expected error for missing uid")
	}
}

func TestTeamRoleNames(t *testing.T) {
	for team, name := range teamNames {
		if TeamFromName(name) != team || TeamName(team) != name {
			t.Errorf("team %v <-> %q", team, name)
		}
	}
	for role, name := range roleNames {
		if RoleFromName(name) != role || RoleName(role) != name {
			t.Errorf("role %v <-> %q", role, name)
		}
	}
	if TeamFromName("nope") != meshpb.Team_Unspecifed_Color || TeamName(meshpb.Team_Unspecifed_Color) != "Cyan" {
		t.Error("unknown team handling")
	}
	if RoleFromName("") != meshpb.MemberRole_Unspecifed || RoleName(meshpb.MemberRole_Unspecifed) != "Team Member" {
		t.Error("unknown role handling")
	}
}

func TestCourseAndClamp(t *testing.T) {
	cases := map[float64]uint32{0: 0, 359.4: 359, 359.6: 0, -10: 350, 720.2: 0}
	for in, want := range cases {
		if got := courseDeg(in); got != want {
			t.Errorf("courseDeg(%v) = %d want %d", in, got, want)
		}
	}
	ev := pliEvent()
	ev.Lat, ev.Lon = 95, -200
	pkt, _ := FromEvent(ev, nil)
	if pkt.GetPli().GetLatitudeI() != 900000000 || pkt.GetPli().GetLongitudeI() != -1800000000 {
		t.Errorf("clamp failed: %v", pkt.GetPli())
	}
}

func TestDecodeGarbage(t *testing.T) {
	if _, err := Decode([]byte{0xff, 0xff, 0xff}); err == nil {
		t.Error("expected error")
	}
	// A compressed packet decodes fine; callers decide what to do with it.
	b, _ := proto.Marshal(&meshpb.TAKPacket{IsCompressed: true})
	pkt, err := Decode(b)
	if err != nil || !pkt.GetIsCompressed() {
		t.Errorf("compressed decode: %v %v", pkt, err)
	}
}
