package profiles_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/profiles"
)

func TestEvaluateValidatesFrontedREALITYClientValues(t *testing.T) {
	validKey := realityKey
	validSecurity := fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"abcd"}`, validKey)
	tests := []struct {
		name     string
		security string
	}{
		{name: "server name", security: fmt.Sprintf(`{"type":"reality","server_name":"https://cover.example.com","public_key":%q,"short_id":"abcd"}`, validKey)},
		{name: "public key", security: `{"type":"reality","server_name":"cover.example.com","public_key":"not-a-key","short_id":"abcd"}`},
		{name: "short ID", security: fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"abc"}`, validKey)},
		{name: "post-quantum verify", security: fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"abcd","post_quantum_verify":"not-a-key"}`, validKey)},
		{name: "spider X", security: fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":"abcd","spider_x":"missing-slash"}`, validKey)},
		{name: "TLS verify name", security: `{"type":"tls","verify_name":"https://verify.example.com"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "tls", "")))
			advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "fronted", `{"type":"tcp"}`, test.security)))
			got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
				XrayView: xray, XrayAvailable: true,
				AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
			})
			if got.Items[0].Unavailable == nil || got.Items[0].Unavailable.Reason != profiles.ReasonAdvertisementInvalid {
				t.Errorf("result = %+v, want advertisement_invalid", got)
			}
		})
	}

	t.Run("valid values", func(t *testing.T) {
		xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "tls", "")))
		advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "fronted", `{"type":"tcp"}`, validSecurity)))
		got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
			XrayView: xray, XrayAvailable: true,
			AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
		})
		if got.Items[0].Available == nil {
			t.Errorf("result = %+v, want available profile", got)
		}
	})
}

func TestEvaluateRejectsFrontedEmptyREALITYShortIDUnlessInboundAcceptsIt(t *testing.T) {
	security := fmt.Sprintf(`{"type":"reality","server_name":"cover.example.com","public_key":%q,"short_id":""}`, realityKey)
	advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "fronted", `{"type":"tcp"}`, security)))
	for _, test := range []struct {
		name          string
		shortIDs      string
		wantReason    profiles.Reason
		wantAvailable bool
	}{
		{name: "not accepted", shortIDs: `[]`, wantReason: profiles.ReasonInboundMismatch},
		{name: "accepted", shortIDs: `[""]`, wantAvailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "reality", `,"realitySettings":{"serverNames":["other.example.com"],"shortIds":`+test.shortIDs+`}`)))
			got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
				XrayView: xray, XrayAvailable: true,
				AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
			})
			if test.wantAvailable {
				if got.Items[0].Available == nil {
					t.Errorf("result = %+v, want available profile", got)
				}
				return
			}
			if got.Items[0].Unavailable == nil || got.Items[0].Unavailable.Reason != test.wantReason {
				t.Errorf("result = %+v, want %s", got, test.wantReason)
			}
		})
	}
}

func TestEvaluateKeepsDuplicateAdvertisementsLocalToSelectedInbound(t *testing.T) {
	xray := parseXray(t, xrayDocument(
		inboundJSON("duplicate", fixtureID, "", "none", "raw", "tls", ""),
		inboundJSON("healthy", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", "", "none", "raw", "tls", ""),
	))
	advertised := parseAdvertisements(t, advertisementDocument(
		advertisementJSON("duplicate", "direct", `{"type":"tcp"}`, `{"type":"tls"}`),
		advertisementJSON("duplicate", "direct", `{"type":"tcp"}`, `{"type":"tls"}`),
		advertisementJSON("healthy", "direct", `{"type":"tcp"}`, `{"type":"tls"}`),
	))
	got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
		XrayView: xray, XrayAvailable: true,
		AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
	})
	if len(got.Items) != 2 || got.Items[0].Unavailable == nil ||
		got.Items[0].Unavailable.Reason != profiles.ReasonDuplicateInboundTag || got.Items[1].Available == nil {
		t.Errorf("ordered results = %+v, want duplicate unavailable then healthy available", got.Items)
	}
}

func TestEvaluateReturnsOneDuplicateTagFailureAtEachInboundPosition(t *testing.T) {
	xray := parseXray(t, xrayDocument(
		inboundJSON("same", fixtureID, "", "none", "raw", "tls", ""),
		inboundJSON("same", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", "", "none", "raw", "tls", ""),
	))
	advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("same", "direct", `{"type":"tcp"}`, `{"type":"tls"}`)))
	got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
		XrayView: xray, XrayAvailable: true,
		AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
	})
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want two inbound positions", len(got.Items))
	}
	for index, item := range got.Items {
		if item.Unavailable == nil || item.Unavailable.Reason != profiles.ReasonDuplicateInboundTag {
			t.Errorf("item %d = %+v, want duplicate_inbound_tag", index, item)
		}
	}
}

func TestEvaluateMatchesExactEmailIdentity(t *testing.T) {
	xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "tls", "")))
	got := profiles.Evaluate(strings.ToUpper(fixtureEmail), false, profiles.Sources{XrayView: xray, XrayAvailable: true})
	if got.State != profiles.StateNoMatchingInbound {
		t.Errorf("state = %q, want exact-email no_matching_inbound", got.State)
	}
}
