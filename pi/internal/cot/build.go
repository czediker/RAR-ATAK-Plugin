package cot

import (
	"encoding/xml"
	"math"
	"strconv"
	"strings"
	"time"
)

// DefaultPLIType is the CoT type used for positions received over
// Meshtastic. TAKPacket does not carry the original type.
const DefaultPLIType = "a-f-G-U-C"

// Via describes where a rebuilt event came from. It is written into the
// event's detail as <__rar via="..." node="..."/> so received traffic can be
// told apart from native HaLow traffic.
type Via struct {
	Transport string // e.g. "meshtastic"
	Node      string // e.g. "!a1b2c3d4"
}

// PLI describes a position report to render as CoT.
type PLI struct {
	UID      string
	Callsign string
	Type     string // defaults to DefaultPLIType
	Lat, Lon float64
	HAE      float64 // Unknown when not known
	Team     string
	Role     string
	Battery  int     // -1 when unknown
	Speed    float64 // m/s, NaN when unknown
	Course   float64 // degrees, NaN when unknown
	Time     time.Time
	Stale    time.Duration
	Via      Via
}

// BuildPLI renders a position report as a CoT event.
func BuildPLI(p PLI) []byte {
	typ := p.Type
	if typ == "" {
		typ = DefaultPLIType
	}
	b := newBuilder()
	b.openEvent(p.UID, typ, "m-g", p.Time, p.Stale)
	b.point(p.Lat, p.Lon, p.HAE, Unknown, Unknown)
	b.raw("<detail>")
	if p.Callsign != "" {
		b.element("contact", "callsign", p.Callsign)
		b.element("uid", "Droid", p.Callsign)
	}
	if p.Team != "" || p.Role != "" {
		b.element("__group", "name", p.Team, "role", p.Role)
	}
	if p.Battery >= 0 {
		b.element("status", "battery", strconv.Itoa(p.Battery))
	}
	if !math.IsNaN(p.Speed) || !math.IsNaN(p.Course) {
		b.element("track", "speed", formatFloat(nanToZero(p.Speed), 2), "course", formatFloat(nanToZero(p.Course), 1))
	}
	b.via(p.Via)
	b.raw("</detail></event>")
	return b.bytes()
}

// GeoChat describes a GeoChat message to render as CoT.
type GeoChat struct {
	MessageID      string
	SenderUID      string
	SenderCallsign string
	// ToUID is AllChatRooms (or empty) for broadcast, otherwise the
	// recipient's UID.
	ToUID      string
	ToCallsign string
	Text       string
	// Position of the sender, if known. Zero values place the event at 0,0
	// with unknown accuracy, which ATAK accepts for chat.
	Lat, Lon float64
	HAE      float64
	Time     time.Time
	Stale    time.Duration
	Via      Via
}

// BuildGeoChat renders a GeoChat message as a CoT b-t-f event, matching the
// structure ATAK itself produces so the message lands in the right
// conversation.
func BuildGeoChat(c GeoChat) []byte {
	toUID := c.ToUID
	chatroom := AllChatRooms
	if toUID == "" || toUID == AllChatRooms {
		toUID = AllChatRooms
	} else {
		chatroom = c.ToCallsign
		if chatroom == "" {
			chatroom = toUID
		}
	}
	uid := "GeoChat." + c.SenderUID + "." + toUID + "." + c.MessageID

	b := newBuilder()
	b.openEvent(uid, ChatType, "h-g-i-g-o", c.Time, c.Stale)
	hae := c.HAE
	if hae == 0 && c.Lat == 0 && c.Lon == 0 {
		hae = Unknown
	}
	b.point(c.Lat, c.Lon, hae, Unknown, Unknown)
	b.raw("<detail>")
	b.open("__chat",
		"parent", "RootContactGroup",
		"groupOwner", "false",
		"messageId", c.MessageID,
		"chatroom", chatroom,
		"id", toUID,
		"senderCallsign", c.SenderCallsign)
	b.element("chatgrp", "uid0", c.SenderUID, "uid1", toUID, "id", toUID)
	b.raw("</__chat>")
	b.element("link", "uid", c.SenderUID, "type", DefaultPLIType, "relation", "p-p")
	b.open("remarks",
		"source", "BAO.F.ATAK."+c.SenderUID,
		"to", toUID,
		"time", c.Time.UTC().Format(TimeLayout))
	b.text(c.Text)
	b.raw("</remarks>")
	b.via(c.Via)
	b.raw("</detail></event>")
	return b.bytes()
}

type builder struct{ sb strings.Builder }

func newBuilder() *builder {
	b := &builder{}
	b.sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	return b
}

func (b *builder) raw(s string) { b.sb.WriteString(s) }

func (b *builder) text(s string) {
	var esc strings.Builder
	_ = xml.EscapeText(&esc, []byte(s))
	b.sb.WriteString(esc.String())
}

func (b *builder) attrs(kv []string) {
	for i := 0; i+1 < len(kv); i += 2 {
		b.sb.WriteByte(' ')
		b.sb.WriteString(kv[i])
		b.sb.WriteString(`="`)
		b.text(kv[i+1])
		b.sb.WriteByte('"')
	}
}

func (b *builder) open(name string, kv ...string) {
	b.sb.WriteByte('<')
	b.sb.WriteString(name)
	b.attrs(kv)
	b.sb.WriteByte('>')
}

func (b *builder) element(name string, kv ...string) {
	b.sb.WriteByte('<')
	b.sb.WriteString(name)
	b.attrs(kv)
	b.sb.WriteString("/>")
}

func (b *builder) openEvent(uid, typ, how string, t time.Time, stale time.Duration) {
	t = t.UTC()
	ts := t.Format(TimeLayout)
	b.open("event",
		"version", "2.0",
		"uid", uid,
		"type", typ,
		"how", how,
		"time", ts,
		"start", ts,
		"stale", t.Add(stale).Format(TimeLayout))
}

func (b *builder) point(lat, lon, hae, ce, le float64) {
	b.element("point",
		"lat", formatFloat(lat, 7),
		"lon", formatFloat(lon, 7),
		"hae", formatFloat(hae, 1),
		"ce", formatFloat(ce, 1),
		"le", formatFloat(le, 1))
}

func (b *builder) via(v Via) {
	if v.Transport == "" {
		return
	}
	b.element("__rar", "via", v.Transport, "node", v.Node)
}

func (b *builder) bytes() []byte { return []byte(b.sb.String()) }

func formatFloat(f float64, prec int) string {
	return strconv.FormatFloat(f, 'f', prec, 64)
}

func nanToZero(f float64) float64 {
	if math.IsNaN(f) {
		return 0
	}
	return f
}
