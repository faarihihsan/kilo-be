package middleware

import (
	"context"
	"net/netip"
	"strings"
)

// ClientIP returns the address of the client that made the request, as text,
// for rate limiting and logging.
//
// remoteAddr is http.Request.RemoteAddr (the TCP peer, "ip:port"), xff the
// value of the X-Forwarded-For header (several header lines joined with ","),
// and trusted the ranges of TRUSTED_PROXY_CIDRS.
//
// The header is honored only when the peer itself is a trusted proxy: anyone
// else could write anything into it. Then the list is read from the right,
// each entry being the address a trusted proxy saw its request come from, and
// the result is the first entry that is not itself a trusted proxy. Reading
// from the right means entries a client put at the front cannot win. If the
// walk meets an entry that is not an IP address it stops and returns the last
// address known to be a proxy's (never garbage). Empty entries are skipped. If
// every entry is a trusted proxy the left-most one is returned.
//
// Addresses are normalized: IPv4-mapped IPv6 becomes IPv4, zones and ports are
// dropped. It returns "" when remoteAddr is not an IP address (for example a
// unix socket), which callers treat as one shared "unknown" client.
func ClientIP(remoteAddr, xff string, trusted []netip.Prefix) string {
	peer, ok := clientIPParse(remoteAddr)
	if !ok {
		return ""
	}
	if xff == "" || !clientIPTrusted(peer, trusted) {
		return peer.String()
	}

	// Walk the list from the right without splitting it: a hostile header can
	// be a megabyte of commas, and the walk normally ends after one entry.
	client := peer
	for rest := xff; ; {
		i := strings.LastIndexByte(rest, ',')
		entry := strings.TrimSpace(rest[i+1:])
		if entry != "" {
			addr, ok := clientIPParse(entry)
			if !ok {
				return client.String()
			}
			client = addr
			if !clientIPTrusted(addr, trusted) {
				break
			}
		}
		if i < 0 {
			break
		}
		rest = rest[:i]
	}
	return client.String()
}

// clientIPParse accepts "ip", "ip:port" and "[ip]:port" (also "[ip]").
func clientIPParse(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		if ap, perr := netip.ParseAddrPort(s); perr == nil {
			addr = ap.Addr()
		} else if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			if a, aerr := netip.ParseAddr(s[1 : len(s)-1]); aerr == nil {
				addr = a
			} else {
				return netip.Addr{}, false
			}
		} else {
			return netip.Addr{}, false
		}
	}
	return addr.Unmap().WithZone(""), true
}

func clientIPTrusted(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

type clientIPKey struct{}

// WithClientIP returns a context carrying the resolved client IP. The rate
// limiter middleware sets it on the routes it guards.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIPFrom returns the client IP set by WithClientIP, or "" when the
// request did not pass through the rate limiter middleware (or the peer had no
// IP address). The login handler reads it for the login limiter; an empty
// value is a valid, if coarse, key: auth.IPKey maps it to "unknown".
func ClientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}
