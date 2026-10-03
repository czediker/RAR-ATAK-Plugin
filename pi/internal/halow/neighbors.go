// Package halow decides whether this radio currently has HaLow mesh
// neighbors, using batman-adv's neighbor table.
package halow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Placeholder is the stand-in HaLow interface name used in the default
// configuration. While the configured interface is empty or still the
// placeholder, neighbors on any batman-adv hard interface are counted.
const Placeholder = "HALOW_IFACE"

// Runner executes a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs commands with os/exec.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out, err
}

// Neighbor is one batman-adv neighbor entry.
type Neighbor struct {
	Iface    string
	Address  string
	LastSeen time.Duration
}

// NeighborSource reads batman-adv neighbors via batctl.
type NeighborSource struct {
	Batctl    string        // path to batctl, default "batctl"
	MeshIface string        // batman-adv mesh interface, default "bat0"
	HardIface string        // HaLow hard interface; empty/Placeholder = any
	MaxAge    time.Duration // neighbors not heard for longer are ignored
	Run       Runner        // default ExecRunner

	textOnly bool // set once neighbors_json is found to be unsupported
}

// Neighbors returns the active neighbors on the HaLow interface.
func (s *NeighborSource) Neighbors(ctx context.Context) ([]Neighbor, error) {
	batctl := s.Batctl
	if batctl == "" {
		batctl = "batctl"
	}
	mesh := s.MeshIface
	if mesh == "" {
		mesh = "bat0"
	}
	run := s.Run
	if run == nil {
		run = ExecRunner
	}

	if !s.textOnly {
		out, jerr := run(ctx, batctl, "meshif", mesh, "neighbors_json")
		if jerr == nil {
			all, perr := ParseNeighborsJSON(out)
			if perr == nil {
				return Filter(all, s.HardIface, s.MaxAge), nil
			}
			jerr = perr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Possibly an older batctl without JSON support: try the text
		// output, and stick with it if that works.
		all, terr := s.text(ctx, run, batctl, mesh)
		if terr != nil {
			return nil, fmt.Errorf("%v (text fallback: %v)", jerr, terr)
		}
		s.textOnly = true
		return Filter(all, s.HardIface, s.MaxAge), nil
	}
	all, err := s.text(ctx, run, batctl, mesh)
	if err != nil {
		return nil, err
	}
	return Filter(all, s.HardIface, s.MaxAge), nil
}

func (s *NeighborSource) text(ctx context.Context, run Runner, batctl, mesh string) ([]Neighbor, error) {
	out, err := run(ctx, batctl, "meshif", mesh, "neighbors", "-n", "-H")
	if err != nil {
		return nil, err
	}
	return ParseNeighborsText(out), nil
}

// Filter keeps neighbors on hardIface (any interface when hardIface is
// empty or Placeholder) heard within maxAge (no age limit when maxAge <= 0).
func Filter(all []Neighbor, hardIface string, maxAge time.Duration) []Neighbor {
	anyIface := hardIface == "" || hardIface == Placeholder
	var out []Neighbor
	for _, n := range all {
		if !anyIface && n.Iface != hardIface {
			continue
		}
		if maxAge > 0 && n.LastSeen > maxAge {
			continue
		}
		out = append(out, n)
	}
	return out
}

// ParseNeighborsJSON parses `batctl meshif <if> neighbors_json` output.
func ParseNeighborsJSON(out []byte) ([]Neighbor, error) {
	var rows []struct {
		HardIfname    string `json:"hard_ifname"`
		NeighAddress  string `json:"neigh_address"`
		LastSeenMsecs int64  `json:"last_seen_msecs"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &rows); err != nil {
		return nil, fmt.Errorf("neighbors_json: %w", err)
	}
	ns := make([]Neighbor, 0, len(rows))
	for _, r := range rows {
		ns = append(ns, Neighbor{Iface: r.HardIfname, Address: r.NeighAddress, LastSeen: time.Duration(r.LastSeenMsecs) * time.Millisecond})
	}
	return ns, nil
}

var (
	lastSeenRE = regexp.MustCompile(`(\d+)\.(\d{3})s`)
	// BATMAN_V: "<neighbor> <secs>.<ms>s (<throughput>) [<iface>]"
	bracketIfaceRE = regexp.MustCompile(`\[\s*([^\]\s]+)\s*\]\s*$`)
)

// ParseNeighborsText parses the text output of `batctl n` for both
// BATMAN_IV ("<iface> <neighbor> <last-seen>") and BATMAN_V
// ("<neighbor> <last-seen> (<throughput>) [<iface>]") layouts.
func ParseNeighborsText(out []byte) []Neighbor {
	var ns []Neighbor
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[B.A.T.M.A.N.") || strings.HasPrefix(line, "IF ") || strings.Contains(line, "last-seen") {
			continue
		}
		m := lastSeenRE.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		secs, _ := strconv.Atoi(line[m[2]:m[3]])
		ms, _ := strconv.Atoi(line[m[4]:m[5]])
		n := Neighbor{LastSeen: time.Duration(secs)*time.Second + time.Duration(ms)*time.Millisecond}
		before := strings.Fields(line[:m[0]])
		if im := bracketIfaceRE.FindStringSubmatch(line); im != nil {
			n.Iface = im[1]
			if len(before) > 0 {
				n.Address = before[0]
			}
		} else if len(before) >= 2 {
			n.Iface, n.Address = before[0], before[1]
		}
		ns = append(ns, n)
	}
	return ns
}
