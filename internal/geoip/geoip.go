// Package geoip resolves client IPs to ISO 3166-1 alpha-2 country codes
// from xray's own geoip.dat — the same file xray routes with (ADR-0005).
// The file is loaded once at startup; replacing it takes effect on the
// next panel restart.
package geoip

import (
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"

	"github.com/xtls/xray-core/app/router"
	"google.golang.org/protobuf/proto"
)

// prefixEntry is one geoip.dat CIDR with its country code.
type prefixEntry struct {
	prefix  netip.Prefix
	country string
}

// Resolver answers IP → country lookups against an in-memory copy of
// geoip.dat.
type Resolver struct {
	entries []prefixEntry // sorted by prefix base address
}

// Load reads a geoip.dat (the v2ray protobuf GeoIPList, as shipped by
// xray/Loyalsoldier) and builds the resolver. Only two-letter country
// categories are kept — the file's "private" category and named network
// groups (cloudflare, google, …) are not countries.
func Load(path string) (*Resolver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read geoip.dat: %w", err)
	}
	var list router.GeoIPList
	if err := proto.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse geoip.dat: %w", err)
	}
	resolver := &Resolver{}
	for _, entry := range list.GetEntry() {
		country := strings.ToUpper(entry.GetCountryCode())
		if !isAlpha2(country) {
			continue
		}
		for _, cidr := range entry.GetCidr() {
			addr, ok := netip.AddrFromSlice(cidr.GetIp())
			if !ok {
				continue
			}
			resolver.entries = append(resolver.entries, prefixEntry{
				prefix:  netip.PrefixFrom(addr, int(cidr.GetPrefix())).Masked(),
				country: country,
			})
		}
	}
	slices.SortFunc(resolver.entries, func(a, b prefixEntry) int {
		return a.prefix.Addr().Compare(b.prefix.Addr())
	})
	return resolver, nil
}

// Country returns the ISO alpha-2 code for ip ("" when the IP is unknown,
// private, reserved, or unparseable). geoip.dat's categories partition the
// address space — no CIDR nests inside another — so the containing range,
// if any, is the immediate predecessor by base address.
func (r *Resolver) Country(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	addr = addr.Unmap()
	i, found := slices.BinarySearchFunc(r.entries, addr, func(entry prefixEntry, addr netip.Addr) int {
		return entry.prefix.Addr().Compare(addr)
	})
	if !found {
		i-- // the candidate is the predecessor by base address
	}
	if i < 0 || !r.entries[i].prefix.Contains(addr) {
		return ""
	}
	return r.entries[i].country
}

func isAlpha2(code string) bool {
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}
