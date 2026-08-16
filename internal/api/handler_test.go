package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/yet-an-other/xform/internal/api"
	"github.com/yet-an-other/xform/internal/hoststats"
)

type fixedHostStats struct {
	stats hoststats.Stats
}

func (f fixedHostStats) Latest(context.Context) (hoststats.Stats, error) {
	return f.stats, nil
}

func TestServerEndpointReturnsHostStatsContract(t *testing.T) {
	want := hoststats.Stats{
		CollectedAt:    1_723_800_000,
		CPUPercent:     23.4,
		CPUCores:       4,
		MemUsedBytes:   5_100_273_664,
		MemTotalBytes:  8_589_934_592,
		DiskPath:       "/",
		DiskUsedBytes:  90_194_313_216,
		DiskTotalBytes: 171_798_691_840,
		UptimeSeconds:  1_987_200,
		LoadAvg:        [3]float64{0.42, 0.38, 0.31},
	}
	handler := api.New(fixedHostStats{stats: want}, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/server", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	body := response.Body.Bytes()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode response fields: %v", err)
	}
	wantFields := map[string]struct{}{
		"collected_at": {}, "cpu_percent": {}, "cpu_cores": {},
		"mem_used_bytes": {}, "mem_total_bytes": {}, "disk_path": {},
		"disk_used_bytes": {}, "disk_total_bytes": {}, "uptime_seconds": {},
		"load_avg": {},
	}
	gotFields := make(map[string]struct{}, len(fields))
	for field := range fields {
		gotFields[field] = struct{}{}
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("response fields = %v, want %v", gotFields, wantFields)
	}

	var got hoststats.Stats
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}
