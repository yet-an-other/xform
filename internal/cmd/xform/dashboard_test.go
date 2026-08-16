package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestHandlerServesBuiltDashboardAndAssets(t *testing.T) {
	handler := newDashboardHandler()
	pageRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	pageResponse := httptest.NewRecorder()

	handler.ServeHTTP(pageResponse, pageRequest)

	if pageResponse.Code != http.StatusOK {
		t.Fatalf("page status = %d, want %d", pageResponse.Code, http.StatusOK)
	}
	if got := pageResponse.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("page Content-Type = %q, want text/html", got)
	}
	if !strings.Contains(pageResponse.Body.String(), `<div id="root"></div>`) {
		t.Fatal("page does not contain the React root")
	}

	assetPath := regexp.MustCompile(`src="(/[^"]+\.js)"`).FindStringSubmatch(pageResponse.Body.String())
	if len(assetPath) != 2 {
		t.Fatal("page does not reference a built JavaScript asset")
	}
	assetRequest := httptest.NewRequest(http.MethodGet, assetPath[1], nil)
	assetResponse := httptest.NewRecorder()

	handler.ServeHTTP(assetResponse, assetRequest)

	if assetResponse.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want %d", assetResponse.Code, http.StatusOK)
	}
	if got := assetResponse.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("asset Content-Type = %q, want JavaScript", got)
	}
	if assetResponse.Body.Len() == 0 {
		t.Fatal("JavaScript asset is empty")
	}
}
