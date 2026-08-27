package profiles_test

import (
	"testing"

	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/profiles"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

func TestEvaluateReturnsAvailableProfilesInInboundOrder(t *testing.T) {
	xray := parseXray(t, `{
		"inbounds": [
			{
				"tag": "first",
				"protocol": "vless",
				"settings": {"decryption": "none", "clients": [{"email": "alice@example.com", "id": "1D37A118-4F1B-4DC0-9E3C-3426B07518DF"}]},
				"streamSettings": {"network": "raw", "security": "tls"}
			},
			{
				"tag": "second",
				"protocol": "vless",
				"settings": {"decryption": "none", "clients": [{"email": "alice@example.com", "id": "2d37a118-4f1b-4dc0-9e3c-3426b07518df"}]},
				"streamSettings": {"network": "tcp", "security": "tls"}
			}
		]
	}`)
	advertised := parseAdvertisements(t, `{
		"version": 1,
		"advertisements": [
			{"inbound_tag":"second","name":"Second","topology":"direct","host":"second.example.com","port":443,"transport":{"type":"tcp"},"security":{"type":"tls"}},
			{"inbound_tag":"first","name":"First","topology":"direct","host":"first.example.com","port":8443,"transport":{"type":"tcp"},"security":{"type":"tls"}}
		]
	}`)

	got := profiles.Evaluate("alice@example.com", false, profiles.Sources{
		XrayView:                 xray,
		XrayAvailable:            true,
		AdvertisementsView:       advertised,
		AdvertisementsConfigured: true,
		AdvertisementsAvailable:  true,
	})

	if got.State != profiles.StateReady || len(got.Items) != 2 {
		t.Fatalf("collection = %+v, want two ready items", got)
	}
	if got.Items[0].Available == nil || got.Items[1].Available == nil {
		t.Fatalf("items = %+v, want available profiles", got.Items)
	}
	if got.Items[0].Available.InboundTag != "first" || got.Items[1].Available.InboundTag != "second" {
		t.Errorf("inbound order = %q, %q; want first, second", got.Items[0].Available.InboundTag, got.Items[1].Available.InboundTag)
	}
	wantURI := "vless://1d37a118-4f1b-4dc0-9e3c-3426b07518df@first.example.com:8443?type=tcp&encryption=none&security=tls&fp=chrome&sni=first.example.com#alice%40example.com%20%C2%B7%20First"
	if got.Items[0].Available.URI != wantURI {
		t.Errorf("first URI = %q, want %q", got.Items[0].Available.URI, wantURI)
	}
}

func parseXray(t *testing.T, document string) xrayconfig.View {
	t.Helper()
	view, err := xrayconfig.ParseView([]byte(document))
	if err != nil {
		t.Fatalf("parse xray fixture: %v", err)
	}
	return view
}

func parseAdvertisements(t *testing.T, document string) advertisements.View {
	t.Helper()
	view, err := advertisements.Parse([]byte(document))
	if err != nil {
		t.Fatalf("parse advertisement fixture: %v", err)
	}
	return view
}
