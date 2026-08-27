package profiles_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/profiles"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

func TestEvaluateReportsIndependentSourceFreshnessInFixedOrder(t *testing.T) {
	xrayLoadedAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	advertisementsLoadedAt := xrayLoadedAt.Add(-time.Minute)
	xrayProblem := &xrayconfig.SourceError{Reason: xrayconfig.ParseFailed, Message: "safe xray error"}
	advertisementProblem := &advertisements.SourceError{Reason: advertisements.ReadFailed, Message: "safe advertisement error"}
	xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "tls", "")))
	advertised := parseAdvertisements(t, advertisementDocument(advertisementJSON("main", "direct", `{"type":"tcp"}`, `{"type":"tls"}`)))

	got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
		XrayView: xray, XrayAvailable: true, XrayLoadedAt: xrayLoadedAt, XrayStale: true, XrayError: xrayProblem,
		AdvertisementsView: advertised, AdvertisementsConfigured: true, AdvertisementsAvailable: true,
		AdvertisementsLoadedAt: advertisementsLoadedAt, AdvertisementsStale: true, AdvertisementsError: advertisementProblem,
	})
	if got.LoadedAt == nil || !got.LoadedAt.Equal(advertisementsLoadedAt) || !got.Stale {
		t.Errorf("source freshness = loaded at %v, stale %t", got.LoadedAt, got.Stale)
	}
	wantErrors := []profiles.SourceError{
		{Source: profiles.SourceXrayConfig, Reason: "parse_failed", Message: "safe xray error"},
		{Source: profiles.SourceAdvertisements, Reason: "read_failed", Message: "safe advertisement error"},
	}
	if !reflect.DeepEqual(got.Errors, wantErrors) {
		t.Errorf("errors = %+v, want %+v", got.Errors, wantErrors)
	}
}

func TestEvaluateUsesXrayLoadTimeWhenAdvertisementsHaveNeverLoaded(t *testing.T) {
	loadedAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	xray := parseXray(t, xrayDocument(inboundJSON("main", fixtureID, "", "none", "raw", "tls", "")))
	got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
		XrayView: xray, XrayAvailable: true, XrayLoadedAt: loadedAt,
		AdvertisementsConfigured: true, AdvertisementsAvailable: false,
	})
	if got.LoadedAt == nil || !got.LoadedAt.Equal(loadedAt) || got.Stale {
		t.Errorf("source freshness = loaded at %v, stale %t; want xray time and current", got.LoadedAt, got.Stale)
	}
	if got.Items[0].Unavailable == nil || got.Items[0].Unavailable.Reason != profiles.ReasonSourceUnavailable {
		t.Errorf("items = %+v, want source_unavailable candidate", got.Items)
	}
}

func TestEvaluateHasNoLoadTimeBeforeXrayHasLoaded(t *testing.T) {
	got := profiles.Evaluate(fixtureEmail, false, profiles.Sources{
		AdvertisementsAvailable: true,
		AdvertisementsLoadedAt:  time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC),
	})
	if got.State != profiles.StateSourceUnavailable || got.LoadedAt != nil {
		t.Errorf("collection = %+v, want source_unavailable with nil load time", got)
	}
}
