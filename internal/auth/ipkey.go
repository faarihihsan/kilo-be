package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
)

const (
	// ipv6KeyBits is the prefix length IPv6 addresses are grouped by: one
	// customer (a phone, a home) is usually given a whole /64, so keying on
	// the full address would let one client rotate through 2^64 identities.
	ipv6KeyBits = 64

	// maxKeyLen bounds the size of any limiter key. Longer input (a hostile
	// 500 KB "username") is replaced by its SHA-256, so memory per key is
	// constant.
	maxKeyLen = 64

	unknownIPKey = "unknown"
)

// IPKey is the key under which an IP address is limited: an IPv4 address (also
// when IPv4-mapped in IPv6) as is, an IPv6 address as its /64 network, and
// "unknown" for an empty string. Anything that does not parse is kept as text
// (hashed when long) so it still gets its own bucket. Use it for both the
// login limiter and the generic rate limiter.
func IPKey(ip string) string {
	if ip == "" {
		return unknownIPKey
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return boundKey(ip)
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is6() {
		prefix, _ := addr.Prefix(ipv6KeyBits)
		return prefix.String()
	}
	return addr.String()
}

// boundKey returns s, or a fixed-size digest of it when s is longer than
// maxKeyLen.
func boundKey(s string) string {
	if len(s) <= maxKeyLen {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
