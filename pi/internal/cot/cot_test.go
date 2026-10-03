package cot

import (
	"math"
	"strings"
	"testing"
	"time"
)

const samplePLI = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><event version="2.0" uid="ANDROID-589520ccfcd20f01" type="a-f-G-U-C" time="2026-10-03T18:21:37.582Z" start="2026-10-03T18:21:37.582Z" stale="2026-10-03T18:27:52.582Z" how="h-e"><point lat="38.8977" lon="-77.0365" hae="25.3" ce="9999999.0" le="9999999.0"/><detail><takv os="34" version="5.8.0.5 (abcdef).1700000000-CIV" device="SAMSUNG SM-S918U" platform="ATAK-CIV"/><contact endpoint="10.41.113.200:4242:tcp" callsign="ALPHA"/><uid Droid="ALPHA"/><precisionlocation altsrc="GPS" geopointsrc="GPS"/><__group role="Team Lead" name="Cyan"/><status battery="88"/><track course="123.4" speed="1.5"/></detail></event>`

const sampleBroadcastChat = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><event version="2.0" uid="GeoChat.ANDROID-589520ccfcd20f01.All Chat Rooms.5d1f2a3b-1111-2222-3333-444455556666" type="b-t-f" time="2026-10-03T18:22:00.000Z" start="2026-10-03T18:22:00.000Z" stale="2026-10-04T18:22:00.000Z" how="h-g-i-g-o"><point lat="38.8977" lon="-77.0365" hae="25.3" ce="9999999.0" le="9999999.0"/><detail><__chat parent="RootContactGroup" groupOwner="false" messageId="5d1f2a3b-1111-2222-3333-444455556666" chatroom="All Chat Rooms" id="All Chat Rooms" senderCallsign="ALPHA"><chatgrp uid0="ANDROID-589520ccfcd20f01" uid1="All Chat Rooms" id="All Chat Rooms"/></__chat><link uid="ANDROID-589520ccfcd20f01" type="a-f-G-U-C" relation="p-p"/><remarks source="BAO.F.ATAK.ANDROID-589520ccfcd20f01" to="All Chat Rooms" time="2026-10-03T18:22:00.000Z">Hello &amp; welcome</remarks><__serverdestination destinations="10.41.113.200:4242:tcp:ANDROID-589520ccfcd20f01"/><marti/></detail></event>`

const sampleDirectChat = `<event version="2.0" uid="GeoChat.ANDROID-aaa.ANDROID-bbb.msg-1" type="b-t-f" time="2026-10-03T18:22:00.000Z" start="2026-10-03T18:22:00.000Z" stale="2026-10-04T18:22:00.000Z" how="h-g-i-g-o"><point lat="0" lon="0" hae="9999999.0" ce="9999999.0" le="9999999.0"/><detail><__chat parent="RootContactGroup" groupOwner="false" messageId="msg-1" chatroom="BRAVO" id="ANDROID-bbb" senderCallsign="ALPHA"><chatgrp uid0="ANDROID-aaa" uid1="ANDROID-bbb" id="ANDROID-bbb"/></__chat><link uid="ANDROID-aaa" type="a-f-G-U-C" relation="p-p"/><remarks source="BAO.F.ATAK.ANDROID-aaa" to="ANDROID-bbb" time="2026-10-03T18:22:00.000Z">direct</remarks><marti><dest callsign="BRAVO"/></marti></detail></event>`

func TestParsePLI(t *testing.T) {
	ev, err := Parse([]byte(samplePLI))
	if err != nil {
		t.Fatal(err)
	}
	if !ev.IsPLI() || ev.IsChat() {
		t.Fatalf("classification wrong: pli=%v chat=%v", ev.IsPLI(), ev.IsChat())
	}
	if ev.UID != "ANDROID-589520ccfcd20f01" || ev.Callsign != "ALPHA" {
		t.Errorf("uid/callsign = %q/%q", ev.UID, ev.Callsign)
	}
	if ev.Lat != 38.8977 || ev.Lon != -77.0365 || ev.HAE != 25.3 {
		t.Errorf("point = %v,%v,%v", ev.Lat, ev.Lon, ev.HAE)
	}
	if ev.Team != "Cyan" || ev.Role != "Team Lead" || ev.Battery != 88 {
		t.Errorf("group/status = %q/%q/%d", ev.Team, ev.Role, ev.Battery)
	}
	if ev.Speed != 1.5 || ev.Course != 123.4 {
		t.Errorf("track = %v/%v", ev.Speed, ev.Course)
	}
	if want := time.Date(2026, 10, 3, 18, 21, 37, 582e6, time.UTC); !ev.Time.Equal(want) {
		t.Errorf("time = %v, want %v", ev.Time, want)
	}
}

func TestParseBroadcastChat(t *testing.T) {
	ev, err := Parse([]byte(sampleBroadcastChat))
	if err != nil {
		t.Fatal(err)
	}
	if !ev.IsChat() || ev.IsPLI() {
		t.Fatal("expected chat")
	}
	c := ev.Chat
	if c.SenderUID != "ANDROID-589520ccfcd20f01" || c.SenderCallsign != "ALPHA" {
		t.Errorf("sender = %q/%q", c.SenderUID, c.SenderCallsign)
	}
	if !c.IsBroadcast() || c.ToUID != AllChatRooms {
		t.Errorf("to = %q", c.ToUID)
	}
	if c.Text != "Hello & welcome" {
		t.Errorf("text = %q", c.Text)
	}
	if c.MessageID != "5d1f2a3b-1111-2222-3333-444455556666" {
		t.Errorf("message id = %q", c.MessageID)
	}
}

func TestParseDirectChat(t *testing.T) {
	ev, err := Parse([]byte(sampleDirectChat))
	if err != nil {
		t.Fatal(err)
	}
	c := ev.Chat
	if c.IsBroadcast() || c.ToUID != "ANDROID-bbb" || c.Chatroom != "BRAVO" {
		t.Errorf("to = %q room = %q", c.ToUID, c.Chatroom)
	}
	if c.SenderUID != "ANDROID-aaa" {
		t.Errorf("sender = %q", c.SenderUID)
	}
}

func TestParseChatSenderFallbacks(t *testing.T) {
	// No chatgrp and no link: sender comes from the event UID.
	x := `<event version="2.0" uid="GeoChat.ANDROID-ccc.All Chat Rooms.m2" type="b-t-f" time="2026-10-03T18:22:00Z"><point lat="1" lon="2"/><detail><__chat chatroom="All Chat Rooms" id="All Chat Rooms" senderCallsign="CHARLIE"/><remarks>hi</remarks></detail></event>`
	ev, err := Parse([]byte(x))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Chat.SenderUID != "ANDROID-ccc" || !ev.Chat.IsBroadcast() || ev.Chat.Text != "hi" {
		t.Errorf("chat = %+v", ev.Chat)
	}
	if ev.HAE != Unknown {
		t.Errorf("missing hae should be Unknown, got %v", ev.HAE)
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "   ", "not xml", `<event type="a-f"/>`, `<foo uid="x" type="y"/>`} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) expected error", in)
		}
	}
}

func TestBuildPLIRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	out := BuildPLI(PLI{
		UID: "ANDROID-x", Callsign: `A&"B"`, Lat: 1.2345678, Lon: -2.5, HAE: 100,
		Team: "Dark Blue", Role: "Medic", Battery: 50, Speed: 3, Course: 270,
		Time: now, Stale: 5 * time.Minute, Via: Via{Transport: "meshtastic", Node: "!0000abcd"},
	})
	ev, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ev.UID != "ANDROID-x" || ev.Type != DefaultPLIType || ev.Callsign != `A&"B"` {
		t.Errorf("got %+v", ev)
	}
	if ev.Lat != 1.2345678 || ev.Lon != -2.5 || ev.HAE != 100 {
		t.Errorf("point %v %v %v", ev.Lat, ev.Lon, ev.HAE)
	}
	if ev.Team != "Dark Blue" || ev.Role != "Medic" || ev.Battery != 50 || ev.Speed != 3 || ev.Course != 270 {
		t.Errorf("detail %+v", ev)
	}
	if !ev.Stale.Equal(now.Add(5 * time.Minute)) {
		t.Errorf("stale = %v", ev.Stale)
	}
	if !strings.Contains(string(out), `<__rar via="meshtastic" node="!0000abcd"/>`) {
		t.Errorf("missing via marker:\n%s", out)
	}
}

func TestBuildPLIOmitsUnknowns(t *testing.T) {
	out := string(BuildPLI(PLI{UID: "u", Lat: 1, Lon: 2, HAE: Unknown, Battery: -1, Speed: math.NaN(), Course: math.NaN(), Time: time.Now()}))
	for _, s := range []string{"<status", "<track", "<__group", "<contact", "<__rar"} {
		if strings.Contains(out, s) {
			t.Errorf("unexpected %s in %s", s, out)
		}
	}
}

func TestBuildGeoChatRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, to, toCallsign, wantRoom string
	}{
		{"broadcast", AllChatRooms, "", AllChatRooms},
		{"broadcast-empty", "", "", AllChatRooms},
		{"direct", "ANDROID-bbb", "BRAVO", "BRAVO"},
		{"direct-no-callsign", "ANDROID-bbb", "", "ANDROID-bbb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := BuildGeoChat(GeoChat{
				MessageID: "m-1", SenderUID: "ANDROID-aaa", SenderCallsign: "ALPHA",
				ToUID: tc.to, ToCallsign: tc.toCallsign, Text: "<hi> & bye",
				Time: now, Stale: time.Hour,
			})
			ev, err := Parse(out)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !ev.IsChat() {
				t.Fatal("not a chat")
			}
			c := ev.Chat
			if c.Text != "<hi> & bye" || c.SenderUID != "ANDROID-aaa" || c.SenderCallsign != "ALPHA" || c.MessageID != "m-1" {
				t.Errorf("chat = %+v", c)
			}
			if c.Chatroom != tc.wantRoom {
				t.Errorf("chatroom = %q want %q", c.Chatroom, tc.wantRoom)
			}
			wantTo := tc.to
			if wantTo == "" {
				wantTo = AllChatRooms
			}
			if c.ToUID != wantTo {
				t.Errorf("to = %q want %q", c.ToUID, wantTo)
			}
			if !strings.HasPrefix(ev.UID, "GeoChat.ANDROID-aaa."+wantTo+".m-1") {
				t.Errorf("uid = %q", ev.UID)
			}
		})
	}
}
