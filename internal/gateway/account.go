package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// wantsStream reports whether a request body asked for a streamed reply.
func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

type bodyReader struct {
	io.Reader
	io.Closer
}

// sniffStream labels a reply that came without a Content-Type. Some
// upstreams (the ChatGPT backend among them) send their event streams bare;
// the first bytes tell an event stream from a JSON document.
func sniffStream(res *http.Response) {
	if res.Header.Get("Content-Type") != "" {
		return
	}
	br := bufio.NewReader(res.Body)
	head, _ := br.Peek(16)
	t := strings.TrimLeft(string(head), " \t\r\n")
	if strings.HasPrefix(t, "event:") || strings.HasPrefix(t, "data:") {
		res.Header.Set("Content-Type", "text/event-stream")
	} else {
		res.Header.Set("Content-Type", "application/json")
	}
	res.Body = bodyReader{br, res.Body}
}

// collectResponses answers a non-streamed Responses request through a
// streamed upstream: the reply is the response object the stream closes with.
func (s *Server) collectResponses(w http.ResponseWriter, r *http.Request, p provider.Provider, model string, body []byte, u *Usage) (int, string) {
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
	var final json.RawMessage
	var failed string
	readSSE(res.Body, func(_, data string) error {
		var ev struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Message  string          `json:"message"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.completed", "response.incomplete":
			final = ev.Response
		case "response.failed", "error":
			failed = ev.Message
			if ev.Error != nil && ev.Error.Message != "" {
				failed = ev.Error.Message
			}
			if failed == "" {
				failed = "the request failed"
			}
		}
		return nil
	})
	if final == nil {
		if failed == "" {
			failed = "no response from upstream"
		}
		return writeError(w, provider.Responses, 502, p.Name+": "+failed), failed
	}
	sn := newSniffer(provider.Responses, "application/json")
	sn.write(final)
	u.add(sn.usage())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(final)
	return 200, ""
}
