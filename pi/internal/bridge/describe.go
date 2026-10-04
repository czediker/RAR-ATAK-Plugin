package bridge

import (
	"math"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/cot"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshtastic"
	"github.com/czediker/rar-atak-plugin/pi/internal/takconv"
)

// Helpers that turn messages into log attributes for the debug trail.

// eventAttrs describes a CoT event from the plugin: ATAK IDs and metadata.
func eventAttrs(ev *cot.Event) []any {
	if ev.IsChat() {
		c := ev.Chat
		return []any{
			"kind", "chat",
			"sender_uid", c.SenderUID,
			"sender_callsign", c.SenderCallsign,
			"to", c.ToUID,
			"chatroom", c.Chatroom,
			"message_id", c.MessageID,
			"text_bytes", len(c.Text),
			"cot_uid", ev.UID,
		}
	}
	attrs := []any{
		"kind", "position",
		"uid", ev.UID,
		"callsign", ev.Callsign,
		"type", ev.Type,
		"how", ev.How,
		"lat", ev.Lat,
		"lon", ev.Lon,
		"hae", haeString(ev.HAE),
		"team", ev.Team,
		"role", ev.Role,
		"battery", ev.Battery,
	}
	if !math.IsNaN(ev.Speed) {
		attrs = append(attrs, "speed_mps", ev.Speed)
	}
	if !math.IsNaN(ev.Course) {
		attrs = append(attrs, "course", ev.Course)
	}
	if !ev.Stale.IsZero() && !ev.Time.IsZero() {
		attrs = append(attrs, "stale_after", ev.Stale.Sub(ev.Time).Round(time.Second))
	}
	return attrs
}

// packetAttrs describes a TAKPacket: ATAK IDs and metadata.
func packetAttrs(pkt *meshpb.TAKPacket) []any {
	attrs := []any{
		"uid", pkt.GetContact().GetDeviceCallsign(),
		"callsign", pkt.GetContact().GetCallsign(),
	}
	if g := pkt.GetGroup(); g != nil {
		attrs = append(attrs, "team", takconv.TeamName(g.GetTeam()), "role", takconv.RoleName(g.GetRole()))
	}
	if s := pkt.GetStatus(); s != nil {
		attrs = append(attrs, "battery", s.GetBattery())
	}
	switch {
	case pkt.GetPli() != nil:
		p := pkt.GetPli()
		attrs = append(attrs, "kind", "position",
			"lat", float64(p.GetLatitudeI())/1e7,
			"lon", float64(p.GetLongitudeI())/1e7,
			"alt_m", p.GetAltitude(),
			"speed_mps", p.GetSpeed(),
			"course", p.GetCourse())
	case pkt.GetChat() != nil:
		c := pkt.GetChat()
		attrs = append(attrs, "kind", "chat",
			"to", c.GetTo(),
			"to_callsign", c.GetToCallsign(),
			"text_bytes", len(c.GetMessage()))
	case pkt.GetDetail() != nil:
		attrs = append(attrs, "kind", "detail", "detail_bytes", len(pkt.GetDetail()))
	}
	if pkt.GetIsCompressed() {
		attrs = append(attrs, "compressed", true)
	}
	return attrs
}

// meshAttrs describes a received Meshtastic packet's envelope.
func meshAttrs(p *meshpb.MeshPacket) []any {
	attrs := []any{
		"from", meshtastic.NodeID(p.GetFrom()),
		"packet_id", p.GetId(),
		"channel", p.GetChannel(),
		"rssi", p.GetRxRssi(),
		"snr", p.GetRxSnr(),
		"hop_limit", p.GetHopLimit(),
		"hop_start", p.GetHopStart(),
	}
	if p.GetViaMqtt() {
		attrs = append(attrs, "via_mqtt", true)
	}
	return attrs
}

func haeString(h float64) any {
	if h >= cot.Unknown {
		return "unknown"
	}
	return h
}

func kindOf(pkt *meshpb.TAKPacket) string {
	if pkt.GetChat() != nil {
		return "chat"
	}
	return "position"
}
