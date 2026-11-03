package provider

// The connectivity test of an account provider signs its probe the way the
// backend requires: the login's token, the account id, the stream flag.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccountProviderTestSignsAndShapesProbe(t *testing.T) {
	home := isolate(t)
	access := expJWT(t, time.Now().Add(time.Hour))
	writeCodexTokens(t, home, access, "rt")

	var gotAuth, gotAccount, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotAuth, gotAccount, gotBody, gotPath = r.Header.Get("Authorization"), r.Header.Get("chatgpt-account-id"), string(raw), r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	setVar(t, &CodexBase, srv.URL+"/backend-api/codex")

	p, err := Find("codex")
	if err != nil {
		t.Fatalf("codex provider not offered: %v", err)
	}
	res := p.Test(context.Background())
	if len(res) != 1 || res[0].Protocol != Responses {
		t.Fatalf("probes = %+v, want one Responses probe", res)
	}
	if !res[0].OK {
		t.Fatalf("probe failed: status %d, %s", res[0].Status, res[0].Error)
	}
	if gotPath != "/backend-api/codex/responses" {
		t.Errorf("probe path = %q", gotPath)
	}
	if gotAuth != "Bearer "+access || gotAccount != "acct-1" {
		t.Errorf("probe credentials = %q / %q", gotAuth, gotAccount)
	}
	if !strings.Contains(gotBody, `"stream":true`) || !strings.Contains(gotBody, `"store":false`) {
		t.Errorf("probe body not shaped for the backend: %s", gotBody)
	}
	if strings.Contains(gotBody, "max_output_tokens") {
		t.Errorf("probe body still has max_output_tokens: %s", gotBody)
	}
}
