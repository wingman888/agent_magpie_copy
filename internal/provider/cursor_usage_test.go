package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Cursor's current period comes back as its two pools and the total, each
// resetting when the billing cycle ends.
func TestCursorWindows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/aiserver.v1.DashboardService/GetCurrentPeriodUsage" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(rw, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"billingCycleEnd": "1792833042000",
			"planUsage":       map[string]any{"autoPercentUsed": 12.5, "apiPercentUsed": 40, "totalPercentUsed": 20},
		})
	}))
	defer srv.Close()
	old := cursorBase
	cursorBase = srv.URL
	defer func() { cursorBase = old }()

	ws, err := cursorWindows(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[0].Used != 12.5 || ws[1].Used != 40 || ws[2].Used != 20 {
		t.Fatalf("windows = %+v", ws)
	}
	if ws[0].ResetsAt == nil || ws[0].ResetsAt.UnixMilli() != 1792833042000 {
		t.Fatalf("resets = %v", ws[0].ResetsAt)
	}
	if _, err := cursorWindows(context.Background(), "bad"); err == nil {
		t.Fatal("a refused token is an error")
	}
}
