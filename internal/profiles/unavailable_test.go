package profiles_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/profiles"
)

func TestEvaluateProducesEveryUnavailableReason(t *testing.T) {
	validInbound := inboundJSON("main", fixtureID, "", "none", "raw", "tls", "")
	validAdvertisement := advertisementJSON("main", "direct", `{"type":"tcp"}`, `{"type":"tls"}`)
	tests := []struct {
		name       string
		xray       string
		advertised string
		configured bool
		available  bool
		want       profiles.Reason
	}{
		{name: "source unavailable", xray: xrayDocument(validInbound), configured: true, available: false, want: profiles.ReasonSourceUnavailable},
		{name: "advertisement missing", xray: xrayDocument(validInbound), advertised: advertisementDocument(), configured: true, available: true, want: profiles.ReasonAdvertisementMissing},
		{name: "advertisement invalid", xray: xrayDocument(validInbound), advertised: advertisementDocument(`{"inbound_tag":"main","topology":"direct","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}}`), configured: true, available: true, want: profiles.ReasonAdvertisementInvalid},
		{name: "duplicate inbound tag", xray: xrayDocument(validInbound, validInbound), advertised: advertisementDocument(validAdvertisement), configured: true, available: true, want: profiles.ReasonDuplicateInboundTag},
		{name: "duplicate User", xray: xrayDocument(`{"tag":"main","protocol":"vless","settings":{"decryption":"none","clients":[{"email":"alice@example.com","id":"` + fixtureID + `"},{"email":"alice@example.com","id":"other"}]},"streamSettings":{"network":"raw","security":"tls"}}`), advertised: advertisementDocument(validAdvertisement), configured: true, available: true, want: profiles.ReasonDuplicateUser},
		{name: "inbound tag missing", xray: xrayDocument(inboundJSON("", fixtureID, "", "none", "raw", "tls", "")), configured: true, available: false, want: profiles.ReasonInboundTagMissing},
		{name: "reverse User", xray: xrayDocument(inboundJSON("main", fixtureID, `,"reverse":true`, "none", "raw", "tls", "")), advertised: advertisementDocument(validAdvertisement), configured: true, available: true, want: profiles.ReasonReverseUser},
		{name: "unsupported transport", xray: xrayDocument(validInbound), advertised: advertisementDocument(advertisementJSON("main", "fronted", `{"type":"future"}`, `{"type":"tls"}`)), configured: true, available: true, want: profiles.ReasonUnsupportedTransport},
		{name: "unsupported security", xray: xrayDocument(validInbound), advertised: advertisementDocument(advertisementJSON("main", "fronted", `{"type":"tcp"}`, `{"type":"future"}`)), configured: true, available: true, want: profiles.ReasonUnsupportedSecurity},
		{name: "unsupported encryption", xray: xrayDocument(inboundJSON("main", fixtureID, "", "mlkem", "raw", "tls", "")), advertised: advertisementDocument(validAdvertisement), configured: true, available: true, want: profiles.ReasonUnsupportedEncryption},
		{name: "insecure connection", xray: xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "none", "")), advertised: advertisementDocument(advertisementJSON("main", "direct", `{"type":"tcp"}`, `{"type":"none"}`)), configured: true, available: true, want: profiles.ReasonInsecureConnection},
		{name: "inbound mismatch", xray: xrayDocument(inboundJSON("main", fixtureID, "", "none", "ws", "tls", `,"wsSettings":{"path":"/ws","host":"origin.example.com"}`)), advertised: advertisementDocument(validAdvertisement), configured: true, available: true, want: profiles.ReasonInboundMismatch},
		{name: "invalid Client ID", xray: xrayDocument(inboundJSON("main", strings.Repeat("x", 31), "", "none", "raw", "tls", "")), advertised: advertisementDocument(validAdvertisement), configured: true, available: true, want: profiles.ReasonInvalidClientID},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			xray := parseXray(t, test.xray)
			var advertised advertisements.View
			if test.available {
				advertised = parseAdvertisements(t, test.advertised)
			}
			got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
				XrayView: xray, XrayAvailable: true,
				AdvertisementsView: advertised, AdvertisementsConfigured: test.configured, AdvertisementsAvailable: test.available,
			})
			if len(got.Items) == 0 || got.Items[0].Unavailable == nil {
				t.Fatalf("result = %+v, want unavailable item", got)
			}
			if got.Items[0].Unavailable.Reason != test.want {
				t.Errorf("reason = %q, want %q", got.Items[0].Unavailable.Reason, test.want)
			}
			if got.Items[0].Available != nil {
				t.Errorf("unavailable item carries an available profile: %+v", got.Items[0].Available)
			}
		})
	}
}

func TestEvaluateKeepsUserLevelStatesDistinct(t *testing.T) {
	availableXray := parseXray(t, xrayDocument(inboundJSON("other", fixtureID, "", "none", "raw", "tls", "")))
	tests := []struct {
		name      string
		email     string
		disabled  bool
		available bool
		want      profiles.State
	}{
		{name: "disabled User", email: fixtureEmail, disabled: true, available: true, want: profiles.StateDisabledUser},
		{name: "no matching inbound", email: "nobody@example.com", available: true, want: profiles.StateNoMatchingInbound},
		{name: "parsed xray source unavailable", email: fixtureEmail, available: false, want: profiles.StateSourceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := profiles.Evaluate(test.email, test.disabled, profiles.Sources{XrayView: availableXray, XrayAvailable: test.available})
			if got.State != test.want || len(got.Items) != 0 {
				t.Errorf("collection = %+v, want state %q with no items", got, test.want)
			}
		})
	}
}

func inboundJSON(tag, clientID, userExtra, decryption, transport, security, streamExtra string) string {
	return fmt.Sprintf(`{"tag":%q,"protocol":"vless","settings":{"decryption":%q,"clients":[{"email":%q,"id":%q%s}]},"streamSettings":{"network":%q,"security":%q%s}}`,
		tag, decryption, fixtureEmail, clientID, userExtra, transport, security, streamExtra)
}

func xrayDocument(inbounds ...string) string {
	return `{"inbounds":[` + strings.Join(inbounds, ",") + `]}`
}

func advertisementJSON(tag, topology, transport, security string) string {
	return fmt.Sprintf(`{"inbound_tag":%q,"name":"Main","topology":%q,"host":"edge.example.com","port":443,"transport":%s,"security":%s}`,
		tag, topology, transport, security)
}

func advertisementDocument(records ...string) string {
	return `{"version":1,"advertisements":[` + strings.Join(records, ",") + `]}`
}
