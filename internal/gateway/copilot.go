package gateway

// The signed-in GitHub Copilot account as a provider. Its API speaks OpenAI Chat
// Completions at the host the Copilot session names, so every client protocol is
// turned into a Chat request here. Copilot bills a turn the user typed differently
// from a turn that only returns tool output, so the request says which it is.

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/yetone/dial/internal/provider"
)

// copilotMarks reports the headers that tell Copilot what kind of turn this is.
// X-Initiator is "user" when the latest message is something the user typed, and
// "agent" when it only carries tool output back. Vision is set when any part is an image.
func copilotMarks(req *Request) (initiator string, vision bool) {
	initiator = "user"
	if n := len(req.Messages); n > 0 {
		last := req.Messages[n-1]
		for _, p := range last.Parts {
			if p.Kind == ToolResult {
				initiator = "agent"
			}
		}
		if last.Role != "user" {
			initiator = "agent"
		}
	}
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.Kind == Image {
				vision = true
			}
		}
	}
	return initiator, vision
}

// copilot serves a request for a copilot/<model> id.
func (s *Server) copilot(w http.ResponseWriter, r *http.Request, p provider.Provider, from provider.Protocol, model string, body []byte, u *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	token, api, err := provider.CopilotSession(r.Context())
	if err != nil {
		if errors.Is(err, provider.ErrCopilotSignedOut) {
			return writeError(w, from, 401, err.Error()), err.Error()
		}
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
	}
	stream := req.Stream
	up := *req // a copy: the caller still needs the client's own stream flag
	up.Stream = true
	payload := build(provider.Chat, &up, model, provider.HostOf(api))
	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, api+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
	}
	initiator, vision := copilotMarks(req)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	hreq.Header.Set("User-Agent", "dial/"+Version)
	hreq.Header.Set("Editor-Version", "dial/"+Version)
	hreq.Header.Set("Copilot-Integration-Id", "vscode-chat")
	hreq.Header.Set("Authorization", "Bearer "+token)
	hreq.Header.Set("X-Initiator", initiator)
	if vision {
		hreq.Header.Set("Copilot-Vision-Request", "true")
	}
	res, err := s.client.Do(hreq)
	if err != nil {
		return writeError(w, from, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		msg := provider.ErrCopilotSignedOut.Error()
		return writeError(w, from, 401, msg), msg
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeError(w, from, res.StatusCode, msg), msg
	}
	br := bufio.NewReaderSize(res.Body, 32<<10)
	if !codexIsStream(res.Header.Get("Content-Type"), br) {
		b, _ := io.ReadAll(io.LimitReader(br, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeError(w, from, 502, msg), msg
	}
	dec := decoder(provider.Chat)
	if stream {
		enc := encoder(from, newSSEWriter(w), req.Model)
		var failed string
		readSSE(br, func(_, data string) error {
			return dec(data, func(ev Event) {
				switch ev.Kind {
				case KError:
					failed = ev.Text
				case KStart, KUsage:
					u.add(ev.Usage)
				}
				enc.event(ev)
			})
		})
		enc.finish()
		return http.StatusOK, failed
	}
	var col collector
	readSSE(br, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		return writeError(w, from, 502, p.Name+": "+col.err), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	out := render(from, res2, req.Model)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return http.StatusOK, col.err
}
