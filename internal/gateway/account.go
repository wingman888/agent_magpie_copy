package gateway

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/yetone/dial/internal/provider"
)

// wantsStream reports whether the client asked for a streamed reply.
func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// providerError answers a provider reply with an error status. When a
// borrowed login is refused as unauthorized, the client is told to sign in
// again rather than shown the backend's own message. A 403 is passed through
// unchanged, since it may mean something other than a sign-out.
func providerError(w http.ResponseWriter, proto provider.Protocol, p provider.Provider, res *http.Response) (int, string) {
	if p.Account != nil && res.StatusCode == http.StatusUnauthorized {
		msg := provider.SignedOut(p).Error()
		return writeError(w, proto, http.StatusUnauthorized, msg), msg
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	msg := p.Name + ": " + provider.APIError(b, res.Status)
	return writeError(w, proto, res.StatusCode, msg), msg
}

// transportError answers a failure to get a reply from the provider. A login
// that is signed out gets a 401 that says so; anything else is a 502.
func transportError(w http.ResponseWriter, proto provider.Protocol, p provider.Provider, err error) (int, string) {
	if provider.IsSignedOut(err) {
		return writeError(w, proto, http.StatusUnauthorized, err.Error()), err.Error()
	}
	return writeError(w, proto, http.StatusBadGateway, p.Name+": "+err.Error()), err.Error()
}

// aggregateResponses serves a non-streaming Responses request to a borrowed
// Codex login. The backend only streams, so the stream is read to its end and
// the finished response goes back to the client as one JSON object.
func (s *Server) aggregateResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
	res, err := s.forward(r.Context(), p, provider.Responses, pathOf(provider.Responses), rewriteModel(body, model), r.Header)
	if err != nil {
		return transportError(w, provider.Responses, p, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return providerError(w, provider.Responses, p, res)
	}

	var (
		final map[string]any // the response object from the closing event
		items []any          // output items, in case the closing event has none
		fail  string         // the backend's failure, if it reported one
	)
	err = readSSE(res.Body, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Item     json.RawMessage `json:"item"`
			Response map[string]any  `json:"response"`
			Message  string          `json:"message"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.output_item.done":
			var it any
			if json.Unmarshal(ev.Item, &it) == nil && it != nil {
				items = append(items, it)
			}
		case "response.completed", "response.incomplete", "response.failed":
			final = ev.Response
			if e, ok := ev.Response["error"].(map[string]any); ok {
				fail, _ = e["message"].(string)
			}
			if ev.Type == "response.failed" && fail == "" {
				fail = "the response failed"
			}
		case "error":
			fail = ev.Message
			if ev.Error != nil && ev.Error.Message != "" {
				fail = ev.Error.Message
			}
		}
		return nil
	})
	if err != nil {
		return writeError(w, provider.Responses, http.StatusBadGateway, p.Name+": "+err.Error()), err.Error()
	}
	if fail != "" {
		msg := p.Name + ": " + fail
		return writeError(w, provider.Responses, http.StatusBadGateway, msg), fail
	}
	if final == nil {
		msg := p.Name + ": the reply ended before the response completed"
		return writeError(w, provider.Responses, http.StatusBadGateway, msg), msg
	}

	if out, ok := final["output"].([]any); !ok || len(out) == 0 {
		if items == nil {
			items = []any{}
		}
		final["output"] = items
	}
	final["object"] = "response"
	if m, _ := final["model"].(string); m == "" {
		final["model"] = model
	}
	if raw, err := json.Marshal(final["usage"]); err == nil {
		var ru rUsage
		if json.Unmarshal(raw, &ru) == nil {
			u.add(ru.usage())
		}
	}

	b, err := json.Marshal(final)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeError(w, provider.Responses, http.StatusBadGateway, msg), msg
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
	return http.StatusOK, ""
}
