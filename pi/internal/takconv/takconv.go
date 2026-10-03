// Package takconv converts between ATAK CoT events and Meshtastic TAKPacket
// protobufs (the ATAK_PLUGIN port, 72).
//
// Packets are sent uncompressed (is_compressed=false). Current Meshtastic
// firmware passes ATAK_PLUGIN payloads through untouched, so the whole
// encoded TAKPacket has to fit in a single Meshtastic Data payload.
package takconv

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/czediker/rar-atak-plugin/pi/internal/cot"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
)

// MaxPayload is the largest Data payload Meshtastic accepts
// (Constants.DATA_PAYLOAD_LEN).
const MaxPayload = 233

// maxChatText mirrors the firmware's GeoChat.message size limit.
const maxChatText = 200

var (
	// ErrUnsupported is returned for CoT events that are neither PLI nor
	// GeoChat.
	ErrUnsupported = errors.New("takconv: unsupported CoT type")
	// ErrTooLarge is returned when a packet cannot be shrunk to fit.
	ErrTooLarge = errors.New("takconv: packet does not fit in a Meshtastic payload")
)

// Sender carries the sending EUD's group/status, used to fill in chat
// packets (GeoChat CoT does not include them).
type Sender struct {
	Team    string
	Role    string
	Battery int // -1 when unknown
}

// FromEvent converts a PLI or GeoChat CoT event into a TAKPacket. sender is
// optional and only consulted for fields the event itself lacks.
func FromEvent(ev *cot.Event, sender *Sender) (*meshpb.TAKPacket, error) {
	switch {
	case ev.IsChat():
		return chatPacket(ev, sender), nil
	case ev.IsPLI():
		return pliPacket(ev), nil
	default:
		return nil, ErrUnsupported
	}
}

func pliPacket(ev *cot.Event) *meshpb.TAKPacket {
	pkt := &meshpb.TAKPacket{
		Contact: &meshpb.Contact{Callsign: ev.Callsign, DeviceCallsign: ev.UID},
		PayloadVariant: &meshpb.TAKPacket_Pli{Pli: &meshpb.PLI{
			LatitudeI:  degToI(clamp(ev.Lat, -90, 90)),
			LongitudeI: degToI(clamp(ev.Lon, -180, 180)),
			Altitude:   altitude(ev.HAE),
			Speed:      nonNegRound(ev.Speed),
			Course:     courseDeg(ev.Course),
		}},
	}
	if ev.Team != "" || ev.Role != "" {
		pkt.Group = &meshpb.Group{Team: TeamFromName(ev.Team), Role: RoleFromName(ev.Role)}
	}
	if ev.Battery >= 0 {
		pkt.Status = &meshpb.Status{Battery: uint32(min(ev.Battery, 100))}
	}
	return pkt
}

func chatPacket(ev *cot.Event, sender *Sender) *meshpb.TAKPacket {
	c := ev.Chat
	chat := &meshpb.GeoChat{Message: c.Text}
	if c.IsBroadcast() {
		chat.To = proto.String(cot.AllChatRooms)
	} else {
		chat.To = proto.String(c.ToUID)
		if c.Chatroom != "" && c.Chatroom != c.ToUID {
			chat.ToCallsign = proto.String(c.Chatroom)
		}
	}
	pkt := &meshpb.TAKPacket{
		Contact:        &meshpb.Contact{Callsign: c.SenderCallsign, DeviceCallsign: c.SenderUID},
		PayloadVariant: &meshpb.TAKPacket_Chat{Chat: chat},
	}
	if sender != nil {
		if sender.Team != "" || sender.Role != "" {
			pkt.Group = &meshpb.Group{Team: TeamFromName(sender.Team), Role: RoleFromName(sender.Role)}
		}
		if sender.Battery >= 0 {
			pkt.Status = &meshpb.Status{Battery: uint32(min(sender.Battery, 100))}
		}
	}
	return pkt
}

// Marshal encodes a TAKPacket, shrinking it if needed so it fits in
// MaxPayload bytes: chat text is truncated first, then the optional group,
// status and recipient callsign are dropped. The packet is modified in place.
func Marshal(pkt *meshpb.TAKPacket) ([]byte, error) {
	if chat := pkt.GetChat(); chat != nil {
		chat.Message = truncateUTF8(chat.Message, maxChatText)
	}
	for {
		b, err := proto.Marshal(pkt)
		if err != nil {
			return nil, err
		}
		if len(b) <= MaxPayload {
			return b, nil
		}
		over := len(b) - MaxPayload
		chat := pkt.GetChat()
		switch {
		case chat != nil && len(chat.Message) > 0:
			chat.Message = truncateUTF8(chat.Message, max(len(chat.Message)-over, 0))
		case chat != nil && chat.ToCallsign != nil:
			chat.ToCallsign = nil
		case pkt.Status != nil:
			pkt.Status = nil
		case pkt.Group != nil:
			pkt.Group = nil
		default:
			return nil, ErrTooLarge
		}
	}
}

// Decode parses a TAKPacket payload.
func Decode(payload []byte) (*meshpb.TAKPacket, error) {
	var pkt meshpb.TAKPacket
	if err := proto.Unmarshal(payload, &pkt); err != nil {
		return nil, fmt.Errorf("takconv: %w", err)
	}
	return &pkt, nil
}

// SenderUID returns the ATAK UID of the packet's sender.
func SenderUID(pkt *meshpb.TAKPacket) string {
	return pkt.GetContact().GetDeviceCallsign()
}

// Position is a sender location used to place chat events.
type Position struct {
	Lat, Lon, HAE float64
}

// CoTOptions controls how received packets are rendered as CoT.
type CoTOptions struct {
	Now       time.Time
	PLIStale  time.Duration
	ChatStale time.Duration
	Via       cot.Via
	// FromNode and PacketID identify the Meshtastic packet; they seed the
	// GeoChat message ID so the same packet always maps to the same ATAK
	// message (ATAK de-duplicates on it).
	FromNode uint32
	PacketID uint32
	// SenderPos returns the last known position of a sender UID, used to
	// place chat events. Optional.
	SenderPos func(uid string) (Position, bool)
}

// ToCoT renders a received TAKPacket as a CoT event. It returns
// ErrUnsupported for packets other than PLI and plain GeoChat.
func ToCoT(pkt *meshpb.TAKPacket, opts CoTOptions) ([]byte, error) {
	uid := SenderUID(pkt)
	if uid == "" {
		return nil, errors.New("takconv: packet has no sender uid")
	}
	callsign := pkt.GetContact().GetCallsign()
	if pli := pkt.GetPli(); pli != nil {
		p := cot.PLI{
			UID:      uid,
			Callsign: callsign,
			Lat:      float64(pli.GetLatitudeI()) / 1e7,
			Lon:      float64(pli.GetLongitudeI()) / 1e7,
			HAE:      cot.Unknown,
			Battery:  -1,
			Speed:    float64(pli.GetSpeed()),
			Course:   float64(pli.GetCourse()),
			Time:     opts.Now,
			Stale:    opts.PLIStale,
			Via:      opts.Via,
		}
		if alt := pli.GetAltitude(); alt != 0 {
			p.HAE = float64(alt)
		}
		if g := pkt.GetGroup(); g != nil {
			p.Team, p.Role = TeamName(g.GetTeam()), RoleName(g.GetRole())
		}
		if s := pkt.GetStatus(); s != nil {
			p.Battery = int(s.GetBattery())
		}
		return cot.BuildPLI(p), nil
	}
	if chat := pkt.GetChat(); chat != nil {
		if chat.GetReceiptType() != meshpb.GeoChat_ReceiptType_None {
			return nil, ErrUnsupported
		}
		c := cot.GeoChat{
			MessageID:      MessageID(opts.FromNode, opts.PacketID, uid, chat.GetMessage()),
			SenderUID:      uid,
			SenderCallsign: callsign,
			ToUID:          chat.GetTo(),
			ToCallsign:     chat.GetToCallsign(),
			Text:           chat.GetMessage(),
			HAE:            cot.Unknown,
			Time:           opts.Now,
			Stale:          opts.ChatStale,
			Via:            opts.Via,
		}
		if opts.SenderPos != nil {
			if pos, ok := opts.SenderPos(uid); ok {
				c.Lat, c.Lon, c.HAE = pos.Lat, pos.Lon, pos.HAE
			}
		}
		return cot.BuildGeoChat(c), nil
	}
	return nil, ErrUnsupported
}

// MessageID derives a stable UUID-formatted GeoChat message ID from the
// Meshtastic packet identity.
func MessageID(fromNode, packetID uint32, senderUID, text string) string {
	h := sha1.Sum(fmt.Appendf(nil, "%08x|%08x|%s|%s", fromNode, packetID, senderUID, text))
	h[6] = (h[6] & 0x0f) | 0x50 // version 5
	h[8] = (h[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

var teamNames = map[meshpb.Team]string{
	meshpb.Team_White:      "White",
	meshpb.Team_Yellow:     "Yellow",
	meshpb.Team_Orange:     "Orange",
	meshpb.Team_Magenta:    "Magenta",
	meshpb.Team_Red:        "Red",
	meshpb.Team_Maroon:     "Maroon",
	meshpb.Team_Purple:     "Purple",
	meshpb.Team_Dark_Blue:  "Dark Blue",
	meshpb.Team_Blue:       "Blue",
	meshpb.Team_Cyan:       "Cyan",
	meshpb.Team_Teal:       "Teal",
	meshpb.Team_Green:      "Green",
	meshpb.Team_Dark_Green: "Dark Green",
	meshpb.Team_Brown:      "Brown",
}

var roleNames = map[meshpb.MemberRole]string{
	meshpb.MemberRole_TeamMember:      "Team Member",
	meshpb.MemberRole_TeamLead:        "Team Lead",
	meshpb.MemberRole_HQ:              "HQ",
	meshpb.MemberRole_Sniper:          "Sniper",
	meshpb.MemberRole_Medic:           "Medic",
	meshpb.MemberRole_ForwardObserver: "Forward Observer",
	meshpb.MemberRole_RTO:             "RTO",
	meshpb.MemberRole_K9:              "K9",
}

// TeamName returns ATAK's name for a team color ("Cyan" when unspecified).
func TeamName(t meshpb.Team) string {
	if n, ok := teamNames[t]; ok {
		return n
	}
	return "Cyan"
}

// RoleName returns ATAK's name for a role ("Team Member" when unspecified).
func RoleName(r meshpb.MemberRole) string {
	if n, ok := roleNames[r]; ok {
		return n
	}
	return "Team Member"
}

// TeamFromName maps an ATAK team name to the protobuf enum.
func TeamFromName(name string) meshpb.Team {
	for t, n := range teamNames {
		if strings.EqualFold(n, strings.TrimSpace(name)) {
			return t
		}
	}
	return meshpb.Team_Unspecifed_Color
}

// RoleFromName maps an ATAK role name to the protobuf enum.
func RoleFromName(name string) meshpb.MemberRole {
	for r, n := range roleNames {
		if strings.EqualFold(n, strings.TrimSpace(name)) {
			return r
		}
	}
	return meshpb.MemberRole_Unspecifed
}

func degToI(d float64) int32 { return int32(math.Round(d * 1e7)) }

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func altitude(hae float64) int32 {
	if math.IsNaN(hae) || hae >= cot.Unknown || hae <= -cot.Unknown {
		return 0
	}
	return int32(math.Round(clamp(hae, -1e6, 1e6)))
}

func nonNegRound(v float64) uint32 {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	return uint32(math.Round(math.Min(v, 1e6)))
}

func courseDeg(v float64) uint32 {
	if math.IsNaN(v) {
		return 0
	}
	c := math.Mod(math.Round(v), 360)
	if c < 0 {
		c += 360
	}
	return uint32(c)
}

// truncateUTF8 shortens s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
