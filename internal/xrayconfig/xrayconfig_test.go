package xrayconfig_test

import (
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
	if user := roster["alice@example.com"]; user.Protocol != "VLESS" || user.Security != "XTLS-Reality" {
		t.Errorf("alice = %+v, want VLESS / XTLS-Reality (vision flow prefixes the security)", user)
	}
	if user := roster["bob@example.com"]; user.Protocol != "VLESS" || user.Security != "Reality" {
		t.Errorf("bob = %+v, want VLESS / Reality", user)
	}
	if user := roster["carol@example.com"]; user.Protocol != "TROJAN" || user.Security != "TLS" {
		t.Errorf("carol = %+v, want TROJAN / TLS", user)
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

// An email listed on several inbounds keeps the first inbound's labels —
// config order decides, so the parse is stable.
func TestParseKeepsFirstInboundPerEmail(t *testing.T) {
	config := []byte(`{
		"inbounds": [
			{"protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "flow": "xtls-rprx-vision"}]}, "streamSettings": {"security": "reality"}},
			{"protocol": "trojan", "settings": {"clients": [{"email": "alice@example.com"}]}, "streamSettings": {"security": "tls"}}
		]
	}`)

	roster, err := xrayconfig.Parse(config)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if user := roster["alice@example.com"]; user.Protocol != "VLESS" || user.Security != "XTLS-Reality" {
		t.Errorf("alice = %+v, want the first inbound's VLESS / XTLS-Reality", user)
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
	if user := roster["alice@example.com"]; user.Security != "None" {
		t.Errorf("alice security = %q, want None (no streamSettings)", user.Security)
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
