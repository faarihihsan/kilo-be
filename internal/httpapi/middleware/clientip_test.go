package middleware

import (
	"context"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("fd00::/8"),
	}
	tests := []struct {
		name    string
		remote  string
		xff     string
		trusted []netip.Prefix
		want    string
	}{
		// The peer is not a proxy we trust: the header is attacker-controlled.
		{"direct client, no header", "203.0.113.7:5555", "", trusted, "203.0.113.7"},
		{"spoofed header from an untrusted peer", "203.0.113.7:5555", "1.2.3.4", trusted, "203.0.113.7"},
		{"spoofed chain from an untrusted peer", "203.0.113.7:5555", "1.2.3.4, 10.0.0.1, 127.0.0.1", trusted, "203.0.113.7"},
		{"spoofed header claiming to be loopback", "203.0.113.7:5555", "127.0.0.1", trusted, "203.0.113.7"},
		{"untrusted IPv6 peer", "[2001:db8::7]:5555", "1.2.3.4", trusted, "2001:db8::7"},
		{"no trusted proxies configured", "127.0.0.1:5555", "1.2.3.4", nil, "127.0.0.1"},
		{"empty trusted list", "127.0.0.1:5555", "1.2.3.4", []netip.Prefix{}, "127.0.0.1"},

		// Trusted peer: use the header.
		{"trusted peer without header", "127.0.0.1:5555", "", trusted, "127.0.0.1"},
		{"trusted peer, one client", "127.0.0.1:5555", "203.0.113.9", trusted, "203.0.113.9"},
		{"trusted peer, IPv6 client", "127.0.0.1:5555", "2001:db8::9", trusted, "2001:db8::9"},
		{"trusted IPv6 peer", "[fd00::1]:5555", "203.0.113.9", trusted, "203.0.113.9"},
		{"trusted peer in a CIDR", "10.9.8.7:5555", "203.0.113.9", trusted, "203.0.113.9"},
		{"dual-stack listener shows the loopback peer as IPv4-mapped", "[::ffff:127.0.0.1]:5555", "203.0.113.9", trusted, "203.0.113.9"},

		// Right-most entry that is not a proxy; earlier entries are the client's claims.
		{"client-supplied prefix is ignored", "127.0.0.1:5555", "1.1.1.1, 203.0.113.9", trusted, "203.0.113.9"},
		{"spoofed loopback prefix is ignored", "127.0.0.1:5555", "127.0.0.1, 203.0.113.9", trusted, "203.0.113.9"},
		{"trusted hops on the right are skipped", "127.0.0.1:5555", "1.1.1.1, 203.0.113.9, 10.0.0.9, 10.0.0.8", trusted, "203.0.113.9"},
		{"only trusted hops: the left-most", "127.0.0.1:5555", "10.0.0.1, 10.0.0.2", trusted, "10.0.0.1"},
		{"IPv6 hop chain", "127.0.0.1:5555", "2001:db8::1, fd00::2", trusted, "2001:db8::1"},

		// Normalization.
		{"IPv4-mapped entry", "127.0.0.1:5555", "::ffff:203.0.113.9", trusted, "203.0.113.9"},
		{"entry with port", "127.0.0.1:5555", "203.0.113.9:4711", trusted, "203.0.113.9"},
		{"bracketed IPv6 entry with port", "127.0.0.1:5555", "[2001:db8::1]:443", trusted, "2001:db8::1"},
		{"bracketed IPv6 entry", "127.0.0.1:5555", "[2001:db8::1]", trusted, "2001:db8::1"},
		{"entry with zone", "127.0.0.1:5555", "fe80::1%eth0", trusted, "fe80::1"},
		{"whitespace around entries", "127.0.0.1:5555", "  1.1.1.1 ,\t 203.0.113.9  ", trusted, "203.0.113.9"},
		{"upper case IPv6", "127.0.0.1:5555", "2001:DB8::A", trusted, "2001:db8::a"},
		{"peer with zone", "[fe80::1%eth0]:5555", "", trusted, "fe80::1"},
		{"peer without port", "203.0.113.7", "", trusted, "203.0.113.7"},

		// Malformed entries never become the client.
		{"garbage only", "127.0.0.1:5555", "unknown", trusted, "127.0.0.1"},
		{"garbage right-most", "127.0.0.1:5555", "203.0.113.9, garbage", trusted, "127.0.0.1"},
		{"garbage behind a proxy hop", "127.0.0.1:5555", "203.0.113.9, garbage, 10.0.0.9", trusted, "10.0.0.9"},
		{"garbage left of the client is never reached", "127.0.0.1:5555", "garbage, 203.0.113.9", trusted, "203.0.113.9"},
		{"hostname", "127.0.0.1:5555", "evil.example.com", trusted, "127.0.0.1"},
		{"truncated address", "127.0.0.1:5555", "203.0.113", trusted, "127.0.0.1"},
		{"address with trailing text", "127.0.0.1:5555", "203.0.113.9 (client)", trusted, "127.0.0.1"},
		{"too many octets", "127.0.0.1:5555", "203.0.113.9.9", trusted, "127.0.0.1"},
		{"octet out of range", "127.0.0.1:5555", "256.1.1.1", trusted, "127.0.0.1"},
		{"CIDR instead of an address", "127.0.0.1:5555", "203.0.113.0/24", trusted, "127.0.0.1"},
		{"open bracket", "127.0.0.1:5555", "[2001:db8::1", trusted, "127.0.0.1"},
		{"empty brackets", "127.0.0.1:5555", "[]", trusted, "127.0.0.1"},
		{"NUL byte", "127.0.0.1:5555", "203.0.113.9\x00", trusted, "127.0.0.1"},
		{"empty entries are skipped", "127.0.0.1:5555", "203.0.113.9,,", trusted, "203.0.113.9"},
		{"only commas", "127.0.0.1:5555", ",,,", trusted, "127.0.0.1"},
		{"only spaces", "127.0.0.1:5555", "   ", trusted, "127.0.0.1"},

		// No usable peer address.
		{"empty remote address", "", "1.2.3.4", trusted, ""},
		{"unix socket", "@", "1.2.3.4", trusted, ""},
		{"garbage remote address", "not-an-address", "1.2.3.4", trusted, ""},
		{"hostname remote address", "localhost:8080", "1.2.3.4", trusted, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientIP(tc.remote, tc.xff, tc.trusted); got != tc.want {
				t.Errorf("ClientIP(%q, %q) = %q, want %q", tc.remote, tc.xff, got, tc.want)
			}
		})
	}
}

// A hostile header must not cost much: the walk stops at the first entry that
// is not a trusted proxy and never splits the whole value.
func TestClientIPHandlesHugeHeaders(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

	commas := strings.Repeat(",", 1<<20)
	if got := ClientIP("127.0.0.1:1", "203.0.113.9"+commas, trusted); got != "203.0.113.9" {
		t.Errorf("many trailing commas: %q", got)
	}

	chain := "1.1.1.1," + strings.Repeat("127.0.0.1,", 10_000) + "203.0.113.9"
	if got := ClientIP("127.0.0.1:1", chain, trusted); got != "203.0.113.9" {
		t.Errorf("long chain: %q", got)
	}

	if allocs := testing.AllocsPerRun(20, func() {
		_ = ClientIP("127.0.0.1:1", chain, trusted)
	}); allocs > 10 {
		t.Errorf("%v allocations for a long header, want the walk to stay allocation-light", allocs)
	}
}

func TestClientIPContext(t *testing.T) {
	if got := ClientIPFrom(context.Background()); got != "" {
		t.Errorf("empty context: %q", got)
	}
	ctx := WithClientIP(context.Background(), "203.0.113.9")
	if got := ClientIPFrom(ctx); got != "203.0.113.9" {
		t.Errorf("ClientIPFrom = %q", got)
	}
	// An empty IP is a legitimate value (no peer address).
	if got := ClientIPFrom(WithClientIP(ctx, "")); got != "" {
		t.Errorf("overridden with empty: %q", got)
	}
}
