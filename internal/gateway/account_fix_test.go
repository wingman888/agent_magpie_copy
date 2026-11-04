package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// acctRejectUp answers every request with 401, as the ChatGPT backend does
// for a refused token.
func acctRejectUp(t *testing.T) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"token expired"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(up.Close)
	return up
}

func TestAcctUpstreamRefusedLoginSaysSignedOut(t *testing.T) {
	up := acctRejectUp(t)
	acctCodexLogin(t, up)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 401 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "已登出，请重新登录") {
		t.Errorf("message does not say signed out: %s", body)
	}
}

// A completed event that carries the full output wins over the deltas, which
// here are missing one part of the text.
func TestAcctNonStreamTextFromCompletedOutput(t *testing.T) {
	reply := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_2\",\"model\":\"gpt-5.5\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello \"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"model\":\"gpt-5.5\",\"status\":\"completed\"," +
		"\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello there\"}]}]," +
		"\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"
	b := &acctBackend{reply: reply}
	up := httptest.NewServer(b)
	t.Cleanup(up.Close)
	acctCodexLogin(t, up)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "Hello there") || strings.Contains(body, "Hello Hello") {
		t.Errorf("text not taken from completed output: %s", body)
	}
}
