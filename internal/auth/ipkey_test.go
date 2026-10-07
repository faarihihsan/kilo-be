package auth

import (
	"strings"
	"testing"
)

func TestIPKey(t *testing.T) {
	tests := []struct{ ip, want string }{
		{"192.0.2.1", "192.0.2.1"},
		{"::ffff:192.0.2.1", "192.0.2.1"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:0DB8:0001:0002:ffff:ffff:ffff:ffff", "2001:db8:1:2::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"::1", "::/64"},
		{"", "unknown"},
		{"not an ip", "not an ip"},
	}
	for _, tc := range tests {
		if got := IPKey(tc.ip); got != tc.want {
			t.Errorf("IPKey(%q) = %q, want %q", tc.ip, got, tc.want)
		}
	}

	long := strings.Repeat("x", 10_000)
	if got := IPKey(long); len(got) > 80 || got == long {
		t.Errorf("IPKey of a 10000 byte string has %d bytes, want a short digest", len(got))
	}
	key := IPKey(long)
	if key != IPKey(long) || key == IPKey(long+"y") {
		t.Error("digest is not deterministic or collides")
	}
}
