package gateway

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/yetone/dial/internal/provider"
)

// codexResponses serves a Responses request for a codex/ model. The ChatGPT
// backend only streams, so a non-streaming client gets the gathered final
// response, and a streaming client gets the backend's events as they come.
func (s *Server) codexResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	body = rewriteModel(body, model)
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	if !v.Stream {
		out, err := provider.CodexComplete(r.Context(), p, body)
		if err != nil {
			msg := p.Name + ": " + err.Error()
			return writeError(w, provider.Responses, http.StatusBadGateway, msg), msg
		}
		sniff := newSniffer(provider.Responses, "application/json")
		sniff.write(out)
		u.add(sniff.usage())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(out)
		return http.StatusOK, ""
	}
	res, err := provider.CodexCall(r.Context(), p, body)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, provider.Responses, http.StatusBadGateway, msg), msg
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, provider.Responses, res.StatusCode, msg), msg
	}
	return relayCodex(w, res, p, u)
}

// relayCodex copies the backend's event stream to the client as it arrives,
// reading the usage on the way past. When the reply has no Content-Type, the
// client still gets an event stream if the body is one.
func relayCodex(w http.ResponseWriter, res *http.Response, p provider.Provider, u *Usage) (int, string) {
	src, isStream := eventBody(p, res)
	h := w.Header()
	for _, k := range []string{"Content-Type", "Request-Id", "X-Request-Id"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if isStream {
		if h.Get("Content-Type") == "" {
			h.Set("Content-Type", "text/event-stream")
		}
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(res.StatusCode)
	f, _ := w.(http.Flusher)
	sniff := newSniffer(provider.Responses, h.Get("Content-Type"))
	defer func() { u.add(sniff.usage()) }()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			sniff.write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return res.StatusCode, ""
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	return res.StatusCode, ""
}
