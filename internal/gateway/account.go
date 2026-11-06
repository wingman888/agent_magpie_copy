package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/yetone/dial/internal/provider"
)

// wantsStream reads whether a request asked for a streamed reply.
func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// codexFinal answers a Responses request that did not ask for a stream, sent
// to a Codex account. The Codex backend only streams, so the stream is read
// through and its final response object is returned as plain JSON.
func (s *Server) codexFinal(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	res, err := s.forward(r.Context(), p, provider.Responses, pathOf(provider.Responses), rewriteModel(body, model), r.Header)
	if err != nil {
		return writeError(w, provider.Responses, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}
	final, err := provider.CodexFinal(res.Body)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, provider.Responses, 502, msg), msg
	}
	sn := newSniffer(provider.Responses, "application/json")
	sn.write(final)
	u.add(sn.usage())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, bytes.NewReader(final))
	return http.StatusOK, ""
}
