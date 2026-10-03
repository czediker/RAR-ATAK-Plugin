// Package cot parses and builds the small subset of Cursor-on-Target (CoT)
// XML that the bridge cares about: ATAK position reports (PLI) and GeoChat
// messages.
package cot

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// TimeLayout is the timestamp format ATAK uses in CoT events.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Unknown is ATAK's sentinel for an unknown altitude or circular/linear error.
const Unknown = 9999999.0

// AllChatRooms is the chatroom/uid ATAK uses for broadcast GeoChat messages.
const AllChatRooms = "All Chat Rooms"

// ChatType is the CoT type for a GeoChat message.
const ChatType = "b-t-f"

// Event is the subset of a CoT event the bridge uses.
type Event struct {
	UID   string
	Type  string
	How   string
	Time  time.Time
	Stale time.Time

	Lat, Lon float64
	HAE      float64 // Unknown when not provided
	CE, LE   float64

	Callsign string  // detail/contact@callsign
	Team     string  // detail/__group@name
	Role     string  // detail/__group@role
	Battery  int     // detail/status@battery, -1 when absent
	Speed    float64 // detail/track@speed in m/s, NaN when absent
	Course   float64 // detail/track@course in degrees, NaN when absent

	Chat *Chat // set for GeoChat (b-t-f) events
}

// Chat holds the GeoChat-specific fields of a b-t-f event.
type Chat struct {
	Chatroom       string // __chat@chatroom (room name or recipient callsign)
	ChatroomID     string // __chat@id
	SenderCallsign string // __chat@senderCallsign
	SenderUID      string // chatgrp@uid0, falling back to the link or event UID
	ToUID          string // chatgrp@uid1 ("All Chat Rooms" for broadcast)
	MessageID      string // __chat@messageId
	Text           string // remarks text
}

// IsChat reports whether the event is a GeoChat message.
func (e *Event) IsChat() bool { return e.Type == ChatType && e.Chat != nil }

// IsPLI reports whether the event is an atom (position) report.
func (e *Event) IsPLI() bool { return strings.HasPrefix(e.Type, "a-") }

// IsBroadcast reports whether a chat is addressed to All Chat Rooms.
func (c *Chat) IsBroadcast() bool {
	return c.ToUID == "" || c.ToUID == AllChatRooms
}

type xmlEvent struct {
	XMLName xml.Name `xml:"event"`
	UID     string   `xml:"uid,attr"`
	Type    string   `xml:"type,attr"`
	How     string   `xml:"how,attr"`
	Time    string   `xml:"time,attr"`
	Stale   string   `xml:"stale,attr"`
	Point   struct {
		Lat string `xml:"lat,attr"`
		Lon string `xml:"lon,attr"`
		HAE string `xml:"hae,attr"`
		CE  string `xml:"ce,attr"`
		LE  string `xml:"le,attr"`
	} `xml:"point"`
	Detail struct {
		Contact *struct {
			Callsign string `xml:"callsign,attr"`
		} `xml:"contact"`
		Group *struct {
			Name string `xml:"name,attr"`
			Role string `xml:"role,attr"`
		} `xml:"__group"`
		Status *struct {
			Battery string `xml:"battery,attr"`
		} `xml:"status"`
		Track *struct {
			Speed  string `xml:"speed,attr"`
			Course string `xml:"course,attr"`
		} `xml:"track"`
		Chat *struct {
			Chatroom       string `xml:"chatroom,attr"`
			ID             string `xml:"id,attr"`
			SenderCallsign string `xml:"senderCallsign,attr"`
			MessageID      string `xml:"messageId,attr"`
			ChatGrp        *struct {
				UID0 string `xml:"uid0,attr"`
				UID1 string `xml:"uid1,attr"`
			} `xml:"chatgrp"`
		} `xml:"__chat"`
		Links []struct {
			UID      string `xml:"uid,attr"`
			Relation string `xml:"relation,attr"`
		} `xml:"link"`
		Remarks *struct {
			To   string `xml:"to,attr"`
			Text string `xml:",chardata"`
		} `xml:"remarks"`
	} `xml:"detail"`
}

// Parse decodes a single CoT event.
func Parse(data []byte) (*Event, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("cot: empty payload")
	}
	var x xmlEvent
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("cot: %w", err)
	}
	if x.UID == "" || x.Type == "" {
		return nil, errors.New("cot: event missing uid or type")
	}

	ev := &Event{
		UID:     x.UID,
		Type:    x.Type,
		How:     x.How,
		Time:    parseTime(x.Time),
		Stale:   parseTime(x.Stale),
		Lat:     parseFloat(x.Point.Lat, 0),
		Lon:     parseFloat(x.Point.Lon, 0),
		HAE:     parseFloat(x.Point.HAE, Unknown),
		CE:      parseFloat(x.Point.CE, Unknown),
		LE:      parseFloat(x.Point.LE, Unknown),
		Battery: -1,
		Speed:   math.NaN(),
		Course:  math.NaN(),
	}
	d := &x.Detail
	if d.Contact != nil {
		ev.Callsign = d.Contact.Callsign
	}
	if d.Group != nil {
		ev.Team, ev.Role = d.Group.Name, d.Group.Role
	}
	if d.Status != nil && d.Status.Battery != "" {
		if b, err := strconv.Atoi(strings.TrimSpace(d.Status.Battery)); err == nil {
			ev.Battery = b
		}
	}
	if d.Track != nil {
		ev.Speed = parseFloat(d.Track.Speed, math.NaN())
		ev.Course = parseFloat(d.Track.Course, math.NaN())
	}

	if x.Type == ChatType && d.Chat != nil {
		c := &Chat{
			Chatroom:       d.Chat.Chatroom,
			ChatroomID:     d.Chat.ID,
			SenderCallsign: d.Chat.SenderCallsign,
			MessageID:      d.Chat.MessageID,
		}
		if d.Chat.ChatGrp != nil {
			c.SenderUID = d.Chat.ChatGrp.UID0
			c.ToUID = d.Chat.ChatGrp.UID1
		}
		if c.SenderUID == "" {
			for _, l := range d.Links {
				if l.UID != "" {
					c.SenderUID = l.UID
					break
				}
			}
		}
		if c.SenderUID == "" {
			c.SenderUID = senderFromChatUID(x.UID)
		}
		if c.ToUID == "" {
			c.ToUID = d.Chat.ID
		}
		if d.Remarks != nil {
			c.Text = d.Remarks.Text
			if c.ToUID == "" {
				c.ToUID = d.Remarks.To
			}
		}
		if c.SenderCallsign == "" {
			c.SenderCallsign = ev.Callsign
		}
		ev.Chat = c
	}
	return ev, nil
}

// senderFromChatUID extracts the sender UID from a GeoChat event UID of the
// form "GeoChat.<senderUid>.<chatroom>.<messageId>".
func senderFromChatUID(uid string) string {
	rest, ok := strings.CutPrefix(uid, "GeoChat.")
	if !ok {
		return ""
	}
	if i := strings.Index(rest, "."); i > 0 {
		return rest[:i]
	}
	return ""
}

func parseFloat(s string, def float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) {
		return def
	}
	return f
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, TimeLayout, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
