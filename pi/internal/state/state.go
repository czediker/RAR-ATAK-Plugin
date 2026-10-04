// Package state defines the status file the bridge publishes and the LED
// service reads. Keeping it a plain JSON file on tmpfs keeps the two
// services independent: either can restart without the other noticing.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// DefaultPath is where the bridge writes its status by default.
const DefaultPath = "/var/run/rar/state.json"

// Version is bumped on incompatible changes to Snapshot.
const Version = 2

// HaLow link states, as written to Snapshot.HaLow.State.
const (
	HaLowUnknown   = "unknown"
	HaLowConnected = "connected"
	HaLowIsolated  = "isolated"
)

// Snapshot is the bridge status at a point in time.
type Snapshot struct {
	Version int       `json:"version"`
	Updated time.Time `json:"updated"`

	HaLow struct {
		State     string `json:"state"`
		Neighbors int    `json:"neighbors"`
		// Fault is true while openMANET cannot be queried (batctl fails)
		// or the ATAK multicast listener on the mesh bridge is down.
		Fault bool   `json:"fault"`
		Error string `json:"error,omitempty"`
	} `json:"halow"`

	Meshtastic struct {
		Connected bool   `json:"connected"`
		Node      string `json:"node,omitempty"`
		// Fault is true while the radio is not connected, or shortly after
		// it failed, rejected or did not confirm a packet.
		Fault bool   `json:"fault"`
		Error string `json:"error,omitempty"`
	} `json:"meshtastic"`

	// Forwarding is true when port 6700 traffic is being sent over
	// Meshtastic (the HaLow link is isolated or unknown).
	Forwarding bool `json:"forwarding"`
	// Forced is true when the plugin's "always send over Meshtastic"
	// toggle is in use (port 6701 traffic seen recently).
	Forced bool `json:"forced"`

	// TxCount/RxCount increase with every TAKPacket sent/received over
	// Meshtastic.
	TxCount uint64    `json:"tx_count"`
	RxCount uint64    `json:"rx_count"`
	LastTx  time.Time `json:"last_tx,omitzero"`
	LastRx  time.Time `json:"last_rx,omitzero"`

	EUDs        int `json:"euds"`
	QueuedChats int `json:"queued_chats"`
	QueuedPLIs  int `json:"queued_plis"`
}

// Write atomically replaces the file at path with s.
func Write(path string, s Snapshot) error {
	s.Version = Version
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("write state: %v %v", werr, cerr)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Read loads a snapshot.
func Read(path string) (Snapshot, error) {
	var s Snapshot
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Version != Version {
		return s, fmt.Errorf("%s: unsupported state version %d", path, s.Version)
	}
	return s, nil
}

// Diff lists the fields that differ between two snapshots as
// "field: old → new", ignoring the Updated timestamp. Used for debug logs.
func Diff(old, cur Snapshot) []string {
	a, b := flatten(old), flatten(cur)
	keys := make([]string, 0, len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []string
	for _, k := range keys {
		if k == "updated" || k == "version" {
			continue
		}
		if av, bv := a[k], b[k]; av != bv {
			out = append(out, fmt.Sprintf("%s: %s → %s", k, show(av), show(bv)))
		}
	}
	return out
}

func show(v string) string {
	if v == "" {
		return `""`
	}
	return v
}

// flatten turns a snapshot into "a.b" → value strings via its JSON form.
func flatten(s Snapshot) map[string]string {
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	out := map[string]string{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if mm, ok := v.(map[string]any); ok {
			for k, vv := range mm {
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				walk(key, vv)
			}
			return
		}
		out[prefix] = fmt.Sprint(v)
	}
	walk("", m)
	return out
}
