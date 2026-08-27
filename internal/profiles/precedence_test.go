package profiles_test

import (
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/profiles"
)

func TestEvaluateUsesFixedUnavailablePrecedence(t *testing.T) {
	validAd := advertisementJSON("main", "direct", `{"type":"tcp"}`, `{"type":"tls"}`)
	invalidID := strings.Repeat("x", 31)
	tests := []struct {
		name       string
		xray       string
		advertised string
		configured bool
		available  bool
		want       profiles.Reason
	}{
		{
			name:       "missing tag before duplicate and reverse User",
			xray:       xrayDocument(`{"protocol":"vless","settings":{"clients":[{"email":"alice@example.com","id":"bad","reverse":true},{"email":"alice@example.com","id":"bad"}]}}`),
			configured: true, want: profiles.ReasonInboundTagMissing,
		},
		{
			name: "duplicate tag before duplicate User",
			xray: xrayDocument(
				`{"tag":"main","protocol":"vless","settings":{"clients":[{"email":"alice@example.com","id":"bad"},{"email":"alice@example.com","id":"bad"}]}}`,
				inboundJSON("main", fixtureID, "", "none", "raw", "tls", ""),
			),
			advertised: advertisementDocument(validAd), configured: true, available: true, want: profiles.ReasonDuplicateInboundTag,
		},
		{
			name:       "duplicate User before reverse and source",
			xray:       xrayDocument(`{"tag":"main","protocol":"vless","settings":{"clients":[{"email":"alice@example.com","id":"bad","reverse":true},{"email":"alice@example.com","id":"bad"}]}}`),
			configured: true, want: profiles.ReasonDuplicateUser,
		},
		{
			name:       "reverse before source",
			xray:       xrayDocument(inboundJSON("main", "bad", `,"reverse":true`, "none", "future", "future", "")),
			configured: true, want: profiles.ReasonReverseUser,
		},
		{
			name:       "source before later candidate failures",
			xray:       xrayDocument(inboundJSON("main", invalidID, "", "future", "future", "future", "")),
			configured: true, want: profiles.ReasonSourceUnavailable,
		},
		{
			name:       "missing advertisement before Client ID",
			xray:       xrayDocument(inboundJSON("main", invalidID, "", "future", "future", "future", "")),
			advertised: advertisementDocument(), configured: true, available: true, want: profiles.ReasonAdvertisementMissing,
		},
		{
			name:       "invalid advertisement before Client ID",
			xray:       xrayDocument(inboundJSON("main", invalidID, "", "future", "future", "future", "")),
			advertised: advertisementDocument(`{"inbound_tag":"main","topology":"direct"}`), configured: true, available: true, want: profiles.ReasonAdvertisementInvalid,
		},
		{
			name:       "Client ID before transport",
			xray:       xrayDocument(inboundJSON("main", invalidID, "", "future", "future", "future", "")),
			advertised: advertisementDocument(advertisementJSON("main", "fronted", `{"type":"future"}`, `{"type":"future"}`)), configured: true, available: true, want: profiles.ReasonInvalidClientID,
		},
		{
			name:       "transport before security",
			xray:       xrayDocument(inboundJSON("main", fixtureID, "", "future", "raw", "tls", "")),
			advertised: advertisementDocument(advertisementJSON("main", "fronted", `{"type":"future"}`, `{"type":"future"}`)), configured: true, available: true, want: profiles.ReasonUnsupportedTransport,
		},
		{
			name:       "security before encryption",
			xray:       xrayDocument(inboundJSON("main", fixtureID, "", "future", "raw", "tls", "")),
			advertised: advertisementDocument(advertisementJSON("main", "fronted", `{"type":"tcp"}`, `{"type":"future"}`)), configured: true, available: true, want: profiles.ReasonUnsupportedSecurity,
		},
		{
			name:       "encryption before insecure connection",
			xray:       xrayDocument(inboundJSON("main", fixtureID, "", "future", "raw", "none", "")),
			advertised: advertisementDocument(advertisementJSON("main", "direct", `{"type":"tcp"}`, `{"type":"none"}`)), configured: true, available: true, want: profiles.ReasonUnsupportedEncryption,
		},
		{
			name:       "insecure connection before flow mismatch",
			xray:       xrayDocument(inboundJSON("main", fixtureID, `,"flow":"xtls-rprx-vision"`, "none", "ws", "none", `,"wsSettings":{"path":"/ws","host":"origin.example.com"}`)),
			advertised: advertisementDocument(advertisementJSON("main", "direct", `{"type":"ws","path":"/ws","host":"origin.example.com"}`, `{"type":"none"}`)), configured: true, available: true, want: profiles.ReasonInsecureConnection,
		},
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
		})
	}
}
