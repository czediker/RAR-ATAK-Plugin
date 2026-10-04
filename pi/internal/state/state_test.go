package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	var s Snapshot
	s.Updated = time.Date(2026, 10, 3, 1, 2, 3, 0, time.UTC)
	s.HaLow.State = HaLowIsolated
	s.HaLow.Neighbors = 0
	s.Meshtastic.Connected = true
	s.Meshtastic.Node = "!a1b2c3d4"
	s.Forwarding = true
	s.TxCount = 7
	if err := Write(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != Version || got.HaLow.State != HaLowIsolated || !got.Meshtastic.Connected || got.TxCount != 7 || !got.Updated.Equal(s.Updated) {
		t.Errorf("got %+v", got)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "last_tx") {
		t.Errorf("zero times should be omitted:\n%s", raw)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("dir has %d entries", len(entries))
	}
}

func TestReadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("expected error for missing file")
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{"), 0o644)
	if _, err := Read(bad); err == nil {
		t.Error("expected parse error")
	}
	old := filepath.Join(dir, "old.json")
	os.WriteFile(old, []byte(`{"version": 99}`), 0o644)
	if _, err := Read(old); err == nil {
		t.Error("expected version error")
	}
}

func TestDiff(t *testing.T) {
	var a, b Snapshot
	a.Updated = time.Unix(1, 0)
	a.HaLow.State = HaLowConnected
	a.HaLow.Neighbors = 2
	b = a
	b.Updated = time.Unix(2, 0)
	if d := Diff(a, b); len(d) != 0 {
		t.Errorf("only Updated changed, got %v", d)
	}
	b.HaLow.State = HaLowIsolated
	b.HaLow.Neighbors = 0
	b.Meshtastic.Error = "open: no such file"
	b.TxCount = 3
	got := strings.Join(Diff(a, b), "; ")
	want := `halow.neighbors: 2 → 0; halow.state: connected → isolated; meshtastic.error: "" → open: no such file; tx_count: 0 → 3`
	if got != want {
		t.Errorf("Diff =\n%s\nwant\n%s", got, want)
	}
}
