package journal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSystemd answers the canonical-identity query the way systemd would.
type fakeSystemd struct {
	id      string
	err     error
	asked   []string
	queries int
}

func (f *fakeSystemd) CanonicalID(_ context.Context, name string) (string, error) {
	f.asked = append(f.asked, name)
	f.queries++
	return f.id, f.err
}

func TestResolveXrayUnitTakesSystemdsCanonicalIdentity(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		id         string
		want       string
	}{
		{
			name:       "a plain service name",
			configured: "xray.service",
			id:         "xray.service",
			want:       "xray.service",
		},
		{
			// An alias is exactly why the identity comes from systemd rather
			// than from the configured string.
			name:       "an alias resolves to what it points at",
			configured: "xray-vless.service",
			id:         "xray.service",
			want:       "xray.service",
		},
		{
			name:       "a canonical instance is permitted",
			configured: "xray@edge.service",
			id:         "xray@edge.service",
			want:       "xray@edge.service",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			systemd := &fakeSystemd{id: test.id}

			got, err := ResolveXrayUnit(context.Background(), test.configured, systemd)

			if err != nil {
				t.Fatalf("ResolveXrayUnit() error = %v, want nil", err)
			}
			if got != test.want {
				t.Errorf("unit = %q, want %q", got, test.want)
			}
			if len(systemd.asked) != 1 || systemd.asked[0] != test.configured {
				t.Errorf("asked systemd for %q, want %q", systemd.asked, test.configured)
			}
		})
	}
}

func TestResolveXrayUnitRejectsUnsafeNamesBeforeAskingSystemd(t *testing.T) {
	tests := []struct {
		name       string
		configured string
	}{
		// journalctl --unit= expands globs against every unit in the journal,
		// so a pattern would silently widen what the Panel can read.
		{name: "a glob star", configured: "xray*.service"},
		{name: "a glob question mark", configured: "xray?.service"},
		{name: "a glob class", configured: "xray[12].service"},
		// Shorthand is what journalctl would mangle into a unit name for us;
		// the Panel names units in full or not at all.
		{name: "shorthand without a suffix", configured: "xray"},
		{name: "a non-service unit type", configured: "xray.socket"},
		// A template has no instance to read logs for.
		{name: "an uninstantiated template", configured: "xray@.service"},
		{name: "empty", configured: ""},
		{name: "only whitespace", configured: "   "},
		{name: "a path separator", configured: "../etc/xray.service"},
		{name: "an embedded space", configured: "xray core.service"},
		{name: "an embedded newline", configured: "xray.service\nother.service"},
		{name: "a leading dash that could read as an option", configured: "-xray.service"},
		{name: "longer than systemd allows", configured: strings.Repeat("x", 250) + ".service"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			systemd := &fakeSystemd{id: "xray.service"}

			_, err := ResolveXrayUnit(context.Background(), test.configured, systemd)

			if err == nil {
				t.Fatalf("ResolveXrayUnit(%q) error = nil, want a rejection", test.configured)
			}
			if systemd.queries != 0 {
				t.Errorf("asked systemd %d times, want none — the name is rejected first", systemd.queries)
			}
		})
	}
}

func TestResolveXrayUnitRejectsAnIdentityItCannotTrust(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		queryEr error
	}{
		{
			name:    "systemd cannot answer",
			queryEr: errors.New("no such unit"),
		},
		{
			name: "systemd returns no identity",
			id:   "",
		},
		{
			// systemd answering with another unit type means the configured
			// name did not name the service the Panel thinks it did.
			name: "the identity is not a service",
			id:   "xray.socket",
		},
		{
			name: "the identity is itself a pattern",
			id:   "xray*.service",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			systemd := &fakeSystemd{id: test.id, err: test.queryEr}

			_, err := ResolveXrayUnit(context.Background(), "xray.service", systemd)

			if err == nil {
				t.Fatal("ResolveXrayUnit() error = nil, want a rejection")
			}
		})
	}
}
