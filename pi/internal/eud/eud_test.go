package eud

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	r := NewRegistry(time.Minute)
	t0 := time.Unix(1000, 0)
	a := netip.MustParseAddr("10.41.113.200")
	b := netip.MustParseAddr("10.41.113.201")
	if na, nu := r.Learn(a, "ANDROID-a", t0); !na || !nu {
		t.Error("first Learn should report new address and UID")
	}
	if na, nu := r.Learn(a, "ANDROID-a", t0); na || nu {
		t.Error("repeat Learn should report nothing new")
	}
	r.Learn(netip.MustParseAddr("::ffff:10.41.113.201"), "", t0.Add(30*time.Second))
	r.SetLeases([]netip.Addr{a, netip.MustParseAddr("10.41.113.210")})

	got := r.Targets(t0.Add(40 * time.Second))
	want := []netip.Addr{a, b, netip.MustParseAddr("10.41.113.210")}
	if !slices.Equal(got, want) {
		t.Errorf("targets = %v want %v", got, want)
	}
	if !r.IsLocalUID("ANDROID-a", t0.Add(time.Minute)) || r.IsLocalUID("ANDROID-x", t0) {
		t.Error("IsLocalUID")
	}
	// After TTL, only the still-fresh learned address and leases remain.
	got = r.Targets(t0.Add(61 * time.Second))
	want = []netip.Addr{b, a, netip.MustParseAddr("10.41.113.210")}
	slices.SortFunc(want, func(x, y netip.Addr) int { return x.Compare(y) })
	if !slices.Equal(got, want) {
		t.Errorf("targets after ttl = %v", got)
	}
	if r.IsLocalUID("ANDROID-a", t0.Add(61*time.Second)) || len(r.LocalUIDs(t0.Add(61*time.Second))) != 0 {
		t.Error("uid should have expired")
	}
}

func TestReadLeases(t *testing.T) {
	now := time.Unix(2000, 0)
	path := filepath.Join(t.TempDir(), "dhcp.leases")
	content := "3000 aa:bb:cc:dd:ee:01 10.41.113.200 phone-1 01:aa:bb:cc:dd:ee:01\n" +
		"1000 aa:bb:cc:dd:ee:02 10.41.113.201 expired *\n" +
		"0 aa:bb:cc:dd:ee:03 10.41.113.202 infinite *\n" +
		"3000 aa:bb:cc:dd:ee:04 fd00::1 v6 *\n" +
		"garbage\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLeases(path, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("10.41.113.200"), netip.MustParseAddr("10.41.113.202")}
	if !slices.Equal(got, want) {
		t.Errorf("leases = %v", got)
	}
	if _, err := ReadLeases(filepath.Join(t.TempDir(), "nope"), now); err == nil {
		t.Error("expected error")
	}
}
