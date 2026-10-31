package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// The connectivity test works for an account too, against a stand-in backend.
func TestAcceptCodexAccountConnectivityTest(t *testing.T) {
	home := isolate(t)
	codexLogin(t, home)
	var gotAuth, gotStream string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotStream = r.Header.Get("chatgpt-account-id")
		w.Header()["Content-Type"] = nil
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n")
	}))
	defer up.Close()
	old := codexBase()
	setCodexBase(up.URL + "/backend-api/codex")
	defer setCodexBase(old)

	p, _ := find("codex")
	res := p.Test(context.Background())
	if len(res) == 0 {
		t.Fatal("no results")
	}
	for _, r := range res {
		if !r.OK {
			t.Fatalf("result: %+v", r)
		}
	}
	if gotAuth == "" || gotStream != "acct-1" {
		t.Fatalf("not signed: %q %q", gotAuth, gotStream)
	}
}

func codexBase() string     { return provider.CodexBase }
func setCodexBase(s string) { provider.CodexBase = s }
