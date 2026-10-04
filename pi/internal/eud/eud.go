// Package eud tracks the end-user devices (phones running ATAK) attached to
// this radio, without any configured IP addresses: devices are learned from
// the plugin's traffic and, optionally, from this node's DHCP leases.
package eud

import (
	"bufio"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultLeaseFile is dnsmasq's lease file on OpenWrt.
const DefaultLeaseFile = "/tmp/dhcp.leases"

// Registry holds the local EUDs. It is not safe for concurrent use; the
// bridge's event loop owns it.
type Registry struct {
	// TTL is how long a learned device or UID stays known after its last
	// packet.
	TTL time.Duration

	addrs  map[netip.Addr]time.Time
	uids   map[string]time.Time
	leases []netip.Addr
}

// NewRegistry creates an empty registry.
func NewRegistry(ttl time.Duration) *Registry {
	return &Registry{TTL: ttl, addrs: map[netip.Addr]time.Time{}, uids: map[string]time.Time{}}
}

// Learn records that addr sent traffic for ATAK UID uid (uid may be empty).
// It reports whether the address and the UID were not known before.
func (r *Registry) Learn(addr netip.Addr, uid string, now time.Time) (newAddr, newUID bool) {
	r.expire(now)
	if addr.IsValid() {
		a := addr.Unmap()
		_, known := r.addrs[a]
		newAddr = !known
		r.addrs[a] = now
	}
	if uid != "" {
		_, known := r.uids[uid]
		newUID = !known
		r.uids[uid] = now
	}
	return newAddr, newUID
}

// SetLeases replaces the set of addresses taken from DHCP leases.
func (r *Registry) SetLeases(addrs []netip.Addr) { r.leases = addrs }

// Targets returns the addresses received traffic should be delivered to.
func (r *Registry) Targets(now time.Time) []netip.Addr {
	r.expire(now)
	out := make([]netip.Addr, 0, len(r.addrs)+len(r.leases))
	for a := range r.addrs {
		out = append(out, a)
	}
	for _, a := range r.leases {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return out
}

// IsLocalUID reports whether uid belongs to an EUD on this radio.
func (r *Registry) IsLocalUID(uid string, now time.Time) bool {
	r.expire(now)
	_, ok := r.uids[uid]
	return ok
}

// LocalUIDs returns the known local ATAK UIDs.
func (r *Registry) LocalUIDs(now time.Time) []string {
	r.expire(now)
	out := make([]string, 0, len(r.uids))
	for u := range r.uids {
		out = append(out, u)
	}
	slices.Sort(out)
	return out
}

func (r *Registry) expire(now time.Time) {
	for a, t := range r.addrs {
		if now.Sub(t) > r.TTL {
			delete(r.addrs, a)
		}
	}
	for u, t := range r.uids {
		if now.Sub(t) > r.TTL {
			delete(r.uids, u)
		}
	}
}

// ReadLeases returns the IPv4 addresses of unexpired leases in a dnsmasq
// lease file ("<expiry> <mac> <ip> <hostname> <client-id>" per line).
func ReadLeases(path string, now time.Time) ([]netip.Addr, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []netip.Addr
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		exp, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || (exp != 0 && time.Unix(exp, 0).Before(now)) {
			continue
		}
		a, err := netip.ParseAddr(fields[2])
		if err != nil || !a.Is4() {
			continue
		}
		out = append(out, a)
	}
	return out, sc.Err()
}
