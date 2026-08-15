// Package httpapi exposes xform's HTTP API and embedded dashboard.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/yet-an-other/xform/internal/hoststats"
)

type hostStatsCollector interface {
	Collect(context.Context) (hoststats.Stats, error)
}

// New returns the HTTP handler for the API and dashboard.
func New(collector hostStatsCollector, dashboard http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/server", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		stats, err := collector.Collect(request.Context())
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "host stats unavailable"})
			return
		}

		writeJSON(response, http.StatusOK, stats)
	})
	mux.HandleFunc("/api/", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	mux.Handle("/", dashboard)
	return mux
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
