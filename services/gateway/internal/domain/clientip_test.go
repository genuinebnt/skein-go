package domain

import (
	"net/netip"
	"strings"
	"testing"
)

var testProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"untrusted peer ignores the header", "198.51.100.1:5000", []string{"1.2.3.4"}, "198.51.100.1"},
		{"trusted peer without header", "10.1.1.1:5000", nil, "10.1.1.1"},
		{"one trusted hop", "10.1.1.1:5000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"spoofed left entries are skipped", "10.1.1.1:5000", []string{"6.6.6.6, 203.0.113.9"}, "203.0.113.9"},
		{"chain of trusted proxies", "10.1.1.1:5000", []string{"203.0.113.9, 10.2.2.2", "10.3.3.3"}, "203.0.113.9"},
		{"garbage stops at the last trusted hop", "10.1.1.1:5000", []string{"203.0.113.9, nonsense, 10.2.2.2"}, "10.2.2.2"},
		{"all trusted gives the leftmost", "10.1.1.1:5000", []string{"10.9.9.9, 10.2.2.2"}, "10.9.9.9"},
		{"ipv6 peer", "[2001:db8::1]:443", nil, "2001:db8::1"},
		{"ipv4-mapped peer", "[::ffff:198.51.100.1]:443", nil, "198.51.100.1"},
		{"no port", "198.51.100.1", nil, "198.51.100.1"},
	}
	for _, c := range cases {
		got, ok := ClientIP(c.remote, c.xff, testProxies)
		if !ok || got.String() != c.want {
			t.Errorf("%s: got %v %v, want %s", c.name, got, ok, c.want)
		}
	}
	if _, ok := ClientIP("not an address", nil, testProxies); ok {
		t.Error("unparseable peer should report false")
	}
}

func TestParsePrefixesAcceptsAddressesAndBlocks(t *testing.T) {
	got, err := ParsePrefixes([]string{"10.0.0.0/8", " 192.168.1.5 ", "", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.5/32", "::1/128"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("prefix %d = %s, want %s", i, got[i], want[i])
		}
	}
	if _, err := ParsePrefixes([]string{"10.0.0.0/99"}); err == nil {
		t.Error("bad prefix accepted")
	}
}

// FuzzClientIP checks the security property: an address from
// X-Forwarded-For is only returned when every hop to its right, and the
// peer, are trusted proxies.
func FuzzClientIP(f *testing.F) {
	f.Add("10.1.1.1:80", "6.6.6.6, 203.0.113.9, 10.2.2.2")
	f.Add("198.51.100.1:80", "10.0.0.1")
	f.Add("10.1.1.1:80", ",,, ::1, fd00::2")
	f.Fuzz(func(t *testing.T, remote, header string) {
		got, ok := ClientIP(remote, []string{header}, testProxies)
		if !ok {
			return
		}
		peer, _ := ClientIP(remote, nil, nil)
		if got == peer {
			return
		}
		if !isTrusted(peer, testProxies) {
			t.Fatalf("untrusted peer %v, but returned %v from the header", peer, got)
		}
		hops := strings.Split(header, ",")
		found := false
		for i := len(hops) - 1; i >= 0; i-- {
			addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				t.Fatalf("returned %v past an unparseable hop", got)
			}
			if addr.Unmap() == got {
				found = true
				break
			}
			if !isTrusted(addr.Unmap(), testProxies) {
				t.Fatalf("returned %v left of untrusted hop %v", got, addr)
			}
		}
		if !found {
			t.Fatalf("returned %v, which is not in the header", got)
		}
	})
}
