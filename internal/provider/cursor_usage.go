package provider

// How much of a Cursor plan's included usage is gone, as the CLI's own
// usage view reads it: the dashboard's current period, split into what Auto
// and Composer used and what the named (API) models did.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// cursorBase is Cursor's API; a var so tests can point it elsewhere.
var cursorBase = "https://api2.cursor.sh"

// cursorKeychain reads cursor-agent's token from the macOS Keychain; a var
// so tests stay off the real one.
var cursorKeychain = runtime.GOOS == "darwin"

// cursorToken is the access token cursor-agent signed in with: in the
// Keychain on a Mac, in its auth.json elsewhere.
func cursorToken() (string, error) {
	if cursorKeychain {
		out, err := exec.Command("security", "find-generic-password", "-s", "cursor-access-token", "-a", "cursor-user", "-w").Output()
		if tok := strings.TrimSpace(string(out)); err == nil && tok != "" {
			return tok, nil
		}
	}
	var auth struct {
		AccessToken string `json:"accessToken"`
	}
	if path := cursorAuthPath(); path != "" && readJSON(path, &auth) && auth.AccessToken != "" {
		return auth.AccessToken, nil
	}
	return "", errorf("cursor-agent is not signed in")
}

// cursorAuthPath is where cursor-agent keeps its sign-in outside the Keychain.
func cursorAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "windows":
		dir := os.Getenv("APPDATA")
		if dir == "" {
			dir = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(dir, "Cursor", "auth.json")
	case "darwin":
		return filepath.Join(home, ".cursor", "auth.json")
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cursor", "auth.json")
}

func cursorSubscriptionUsage(ctx context.Context, plan string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "cursor", Name: "Cursor", Icon: "cursor", Plan: plan, Windows: []QuotaWindow{}}
	tok, err := cursorToken()
	if err == nil {
		q.Windows, err = cursorWindows(ctx, tok)
	}
	if err != nil {
		q.Error = err.Error()
	}
	return q
}

// cursorWindows: the plan's included usage this billing period. An
// enterprise plan reports spend instead, and gets no windows.
func cursorWindows(ctx context.Context, token string) ([]QuotaWindow, error) {
	var data struct {
		BillingCycleEnd string `json:"billingCycleEnd"` // epoch millis
		PlanUsage       *struct {
			Auto  float64 `json:"autoPercentUsed"`
			API   float64 `json:"apiPercentUsed"`
			Total float64 `json:"totalPercentUsed"`
		} `json:"planUsage"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorBase+"/aiserver.v1.DashboardService/GetCurrentPeriodUsage", bytes.NewReader([]byte("{}")))
	if err != nil {
		return []QuotaWindow{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return []QuotaWindow{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return []QuotaWindow{}, &accountStatusError{status: res.StatusCode}
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return []QuotaWindow{}, err
	}
	if data.PlanUsage == nil {
		return []QuotaWindow{}, nil
	}
	var resets *time.Time
	if ms, err := strconv.ParseInt(data.BillingCycleEnd, 10, 64); err == nil && ms > 0 {
		t := time.UnixMilli(ms)
		resets = &t
	}
	u := data.PlanUsage
	// the two pools fit the line; the total goes in its tooltip
	return []QuotaWindow{
		{Name: "Auto + Composer", Used: u.Auto, ResetsAt: resets},
		{Name: "API", Used: u.API, ResetsAt: resets},
		{Name: "Total", Used: u.Total, ResetsAt: resets},
	}, nil
}
