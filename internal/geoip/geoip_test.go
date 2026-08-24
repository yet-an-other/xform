package geoip

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/xtls/xray-core/app/router"
	"google.golang.org/protobuf/proto"
)

// writeGeoIPDat serializes a minimal geoip.dat with the given
// country → CIDRs entries, plus the non-country categories a real
// Loyalsoldier file carries.
func writeGeoIPDat(t *testing.T, entries map[string][]string) string {
	t.Helper()
	list := &router.GeoIPList{}
	for country, cidrs := range entries {
		entry := &router.GeoIP{CountryCode: country}
		for _, cidr := range cidrs {
			prefix := netip.MustParsePrefix(cidr)
			entry.Cidr = append(entry.Cidr, &router.CIDR{
				Ip:     prefix.Addr().AsSlice(),
				Prefix: uint32(prefix.Bits()),
			})
		}
		list.Entry = append(list.Entry, entry)
	}
	data, err := proto.Marshal(list)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestCountryResolvesLoadedRanges(t *testing.T) {
	path := writeGeoIPDat(t, map[string][]string{
		"nl":      {"203.0.113.0/24"}, // TEST-NET-3 standing in for an NL block
		"de":      {"198.51.100.0/25"},
		"private": {"10.0.0.0/8", "192.168.0.0/16"},
	})
	resolver, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := map[string]string{
		"203.0.113.10":   "NL",
		"198.51.100.7":   "DE",
		"198.51.100.200": "", // outside the /25
		"10.1.2.3":       "", // private is not a country
		"192.168.1.1":    "", // private is not a country
		"1.1.1.1":        "", // unknown
		"not-an-ip":      "",
	}
	for ip, want := range cases {
		if got := resolver.Country(ip); got != want {
			t.Errorf("Country(%q) = %q, want %q", ip, got, want)
		}
	}
}

func TestCountryWithIPv6(t *testing.T) {
	path := writeGeoIPDat(t, map[string][]string{
		"nl": {"2001:db8::/32"},
	})
	resolver, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := resolver.Country("2001:db8::1"); got != "NL" {
		t.Errorf("Country(v6) = %q, want NL", got)
	}
	if got := resolver.Country("2001:db9::1"); got != "" {
		t.Errorf("Country(v6 outside) = %q, want empty", got)
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(path, []byte("not protobuf"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A garbage file either fails to parse or yields zero usable entries —
	// both mean flags are off, never wrong.
	resolver, err := Load(path)
	if err == nil && resolver.Country("1.1.1.1") != "" {
		t.Error("garbage geoip.dat must not resolve countries")
	}
}

func TestLoadReportsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.dat")); err == nil {
		t.Error("Load of a missing file must fail")
	}
}
