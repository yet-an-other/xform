package xrayconfig_test

import (
	"slices"
	"testing"

	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The roster comes from the xray config's inbounds (SPEC.md §3): each
// client's email is the user identity; protocol labels the inbound,
// security combines the inbound's stream security with the client's flow.
func TestParseBuildsRosterFromInbounds(t *testing.T) {
	config := []byte(`{
		"inbounds": [
			{
				"protocol": "vless",
				"settings": {
					"clients": [
						{"id": "uuid-1", "email": "alice@example.com", "flow": "xtls-rprx-vision"},
						{"id": "uuid-2", "email": "bob@example.com"}
					]
				},
				"streamSettings": {"security": "reality"}
			},
			{
				"protocol": "trojan",
				"settings": {"clients": [{"password": "secret", "email": "carol@example.com"}]},
				"streamSettings": {"security": "tls"}
			}
		]
	}`)

	roster, err := xrayconfig.Parse(config)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(roster) != 3 {
		t.Fatalf("roster = %d users, want 3", len(roster))
	}
	if got := roster["alice@example.com"].Labels; !slices.Equal(got, []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"}}) {
		t.Errorf("alice = %+v, want VLESS / XTLS-Reality (vision flow prefixes the security)", got)
	}
	if got := roster["bob@example.com"].Labels; !slices.Equal(got, []xrayconfig.Label{{Protocol: "VLESS", Security: "Reality", Transport: "tcp"}}) {
		t.Errorf("bob = %+v, want VLESS / Reality", got)
	}
	if got := roster["carol@example.com"].Labels; !slices.Equal(got, []xrayconfig.Label{{Protocol: "TROJAN", Security: "TLS", Transport: "tcp"}}) {
		t.Errorf("carol = %+v, want TROJAN / TLS", got)
	}
}

func TestParseSkipsWhatHasNoIdentity(t *testing.T) {
	config := []byte(`{
		"inbounds": [
			{"protocol": "dokodemo-door", "settings": {}},
			{"protocol": "vless", "settings": {"clients": [{"id": "uuid-1"}]}},
			{"protocol": "vless"}
		]
	}`)

	roster, err := xrayconfig.Parse(config)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(roster) != 0 {
		t.Errorf("roster = %v, want empty — no client carries an email", roster)
	}
}

// A user's row labels cover every inbound their email appears on — one
// label per inbound, config order — so the users table lists each protocol
// the user can connect with, not just the first inbound's.
func TestParseLabelsEveryListedInbound(t *testing.T) {
	config := []byte(`{
		"inbounds": [
			{"tag": "vision", "protocol": "vless",
			 "settings": {"clients": [{"email": "alice@example.com", "flow": "xtls-rprx-vision"}]},
			 "streamSettings": {"network": "raw", "security": "reality"}},
			{"tag": "split", "protocol": "vless",
			 "settings": {"clients": [{"email": "alice@example.com"}]},
			 "streamSettings": {"network": "splithttp", "security": "reality"}},
			{"protocol": "trojan", "settings": {"clients": [{"email": "alice@example.com", "password": "x"}]},
			 "streamSettings": {"security": "tls"}}
		]
	}`)

	roster, err := xrayconfig.Parse(config)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []xrayconfig.Label{
		{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"},
		{Protocol: "VLESS", Security: "Reality", Transport: "xhttp"},
		{Protocol: "TROJAN", Security: "TLS", Transport: "tcp"},
	}
	if got := roster["alice@example.com"].Labels; !slices.Equal(got, want) {
		t.Errorf("alice labels = %+v, want %+v", got, want)
	}
}

// Identical inbound labels collapse to one line — two same-shape inbounds
// are one connection shape, not two.
func TestParseCollapsesIdenticalInboundLabels(t *testing.T) {
	config := []byte(`{
		"inbounds": [
			{"protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "flow": "xtls-rprx-vision"}]},
			 "streamSettings": {"network": "tcp", "security": "reality"}},
			{"protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "flow": "xtls-rprx-vision"}]},
			 "streamSettings": {"network": "raw", "security": "reality"}}
		]
	}`)

	roster, err := xrayconfig.Parse(config)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []xrayconfig.Label{{Protocol: "VLESS", Security: "XTLS-Reality", Transport: "tcp"}}
	if got := roster["alice@example.com"].Labels; !slices.Equal(got, want) {
		t.Errorf("alice labels = %+v, want the one collapsed label", got)
	}
}

// An inbound without stream encryption reports "None" so the column never
// renders a bare protocol with a dangling separator.
func TestParseLabelsUnencryptedSecurity(t *testing.T) {
	config := []byte(`{
		"inbounds": [
			{"protocol": "vless", "settings": {"clients": [{"email": "alice@example.com"}]}}
		]
	}`)

	roster, err := xrayconfig.Parse(config)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := roster["alice@example.com"].Labels; len(got) != 1 || got[0].Security != "None" {
		t.Errorf("alice labels = %+v, want None (no streamSettings)", got)
	}
}

func TestParseRejectsMalformedConfig(t *testing.T) {
	if _, err := xrayconfig.Parse([]byte(`{"inbounds": [`)); err == nil {
		t.Fatal("parse malformed JSON: got nil error, want one")
	}
}

func TestParseRejectsTrailingJSONValue(t *testing.T) {
	if _, err := xrayconfig.Parse([]byte(`{"inbounds": []} {"inbounds": []}`)); err == nil {
		t.Fatal("parse trailing JSON value: got nil error, want one")
	}
}
