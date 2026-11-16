package domain

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// ClientIP returns the address of the client that made a request.
//
// The connecting peer is the client unless it is a trusted proxy. Then
// X-Forwarded-For is read from the right, the hop closest to us, and the
// first address that is not a trusted proxy is the client: everything left
// of it was written by the client or by proxies we know nothing about, and
// trusting it would let a client choose its own rate-limit key. A hop that
// does not parse stops the walk at the last trusted proxy, for the same
// reason. If every hop is trusted, the leftmost is the best we know.
//
// The second result is false only when remoteAddr itself does not parse,
// which net/http never produces for a real connection.
func ClientIP(remoteAddr string, forwardedFor []string, trusted []netip.Prefix) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	peer = peer.Unmap()
	if !isTrusted(peer, trusted) {
		return peer, true
	}
	var hops []string
	for _, header := range forwardedFor {
		for _, hop := range strings.Split(header, ",") {
			hops = append(hops, strings.TrimSpace(hop))
		}
	}
	candidate := peer
	for _, hop := range slices.Backward(hops) {
		addr, err := netip.ParseAddr(hop)
		if err != nil {
			return candidate, true
		}
		candidate = addr.Unmap()
		if !isTrusted(candidate, trusted) {
			return candidate, true
		}
	}
	return candidate, true
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ParsePrefixes reads CIDR blocks ("10.0.0.0/8") or single addresses,
// which become /32 or /128 prefixes.
func ParsePrefixes(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if strings.Contains(v, "/") {
			p, err := netip.ParsePrefix(v)
			if err != nil {
				return nil, err
			}
			prefixes = append(prefixes, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(v)
		if err != nil {
			return nil, err
		}
		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}
