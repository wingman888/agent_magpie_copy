package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// candidate is one place a request may be tried: a provider's key (or a
// signed-in account) serving one of its models.
type candidate struct {
	c     provider.Provider
	model string
}

// candidates lists where a request goes, in order: the provider's enabled
// keys for its model, then the keys of each fallback "provider/model" in the
// order the user listed them. Entries that cannot be read, name no ready
// provider, or repeat an earlier candidate are skipped without a word.
func candidates(p provider.Provider, from provider.Protocol, model string) []candidate {
	var out []candidate
	for _, c := range p.KeyCandidates(from, model) {
		out = append(out, candidate{c, model})
	}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, entry := range p.Fallback {
		pid, fmodel, ok := splitFallback(entry)
		if !ok || seen[pid+"\x00"+fmodel] {
			continue
		}
		seen[pid+"\x00"+fmodel] = true
		fp, ok := readyProvider(pid)
		if !ok {
			continue
		}
		for _, c := range fp.KeyCandidates(from, fmodel) {
			out = append(out, candidate{c, fmodel})
		}
	}
	return out
}

// splitFallback reads a "provider/model" fallback entry. Provider ids hold no
// slash, so the model is everything after the first one.
func splitFallback(entry string) (string, string, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(entry), "/")
	pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
	if !ok || pid == "" || model == "" {
		return "", "", false
	}
	return pid, model, true
}

// readyProvider finds a configured provider by id, ready to take requests.
func readyProvider(id string) (provider.Provider, bool) {
	for _, p := range provider.All() {
		if p.ID == id && p.Ready() {
			return p, true
		}
	}
	return provider.Provider{}, false
}

// retryStatus matches status codes as whole numbers, so "max_tokens 5000"
// does not read as a 500.
var retryStatus = regexp.MustCompile(`\b(402|429|5\d\d)\b`)

// retryText reports whether the text of an error that opened a reply names a
// failure the next candidate should be tried for: a rate limit, exhausted
// quota, overload or an unavailable service.
func retryText(msg string) bool {
	if retryStatus.MatchString(msg) {
		return true
	}
	m := strings.ToLower(msg)
	for _, w := range []string{"rate limit", "rate_limit", "ratelimit", "too many requests", "overload",
		"unavailable", "quota", "insufficient", "balance", "exhaust", "credit", "额度", "余额", "用完", "用尽", "限流", "过载"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

const (
	leadUndecided = iota // not enough of the reply has arrived to tell
	leadOK               // the reply is not an error: send it
	leadError            // the reply opens with an error
)

// leadMax is how much of a reply is held back while it is judged.
const leadMax = 8 << 10

// leadState judges the start of a reply from the complete SSE events in b.
// Control events (ping, message_start, role-only chunks) are skipped; the
// first event that is anything else decides. When the reply is not SSE at
// all, the judgement waits for leadMax bytes.
func leadState(b []byte) (string, int) {
	rest := b
	for {
		end, n := sseEnd(rest)
		if end < 0 {
			if len(b) >= leadMax {
				return "", leadOK
			}
			return "", leadUndecided
		}
		msg, kind := classifyEvent(rest[:end])
		switch kind {
		case leadError:
			return msg, leadError
		case leadOK:
			return "", leadOK
		}
		rest = rest[end+n:]
	}
}

// sseEnd finds the blank line that ends the first SSE event in b, and its
// length.
func sseEnd(b []byte) (int, int) {
	lf := bytes.Index(b, []byte("\n\n"))
	crlf := bytes.Index(b, []byte("\r\n\r\n"))
	switch {
	case crlf >= 0 && (lf < 0 || crlf < lf):
		return crlf, 4
	case lf >= 0:
		return lf, 2
	}
	return -1, 0
}

// classifyEvent reads one complete SSE event. It returns leadBenign for a
// control event, which is skipped; the other kinds decide the reply.
func classifyEvent(ev []byte) (string, int) {
	var event string
	var data []string
	for _, line := range strings.Split(string(ev), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	text := strings.Join(data, "\n")
	if event == "error" || isErrorData(text) {
		if text == "" {
			text = event
		}
		return text, leadError
	}
	if len(data) == 0 && event == "" {
		return "", leadBenign
	}
	switch event {
	case "ping", "message_start", "response.created", "response.in_progress":
		return "", leadBenign
	}
	if isBenignData(text) {
		return "", leadBenign
	}
	return "", leadOK
}

// leadBenign is a control event that does not yet decide the reply. It sits
// beside the three states leadState reports; it never escapes it.
const leadBenign = -1

// isErrorData reports whether an event's JSON data is an error.
func isErrorData(data string) bool {
	var v struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return false
	}
	return v.Type == "error" || (len(v.Error) > 0 && string(v.Error) != "null")
}

// isBenignData reports whether an event's JSON data only opens the reply: a
// ping, the start of a message, or a chunk with nothing but a role.
func isBenignData(data string) bool {
	var v struct {
		Type    string `json:"type"`
		Choices []struct {
			Delta        map[string]json.RawMessage `json:"delta"`
			FinishReason json.RawMessage            `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return false
	}
	switch v.Type {
	case "ping", "message_start", "response.created", "response.in_progress":
		return true
	}
	if len(v.Choices) == 0 {
		return false
	}
	for _, ch := range v.Choices {
		for k := range ch.Delta {
			if k != "role" {
				return false
			}
		}
		if len(ch.FinishReason) > 0 && string(ch.FinishReason) != "null" {
			return false
		}
	}
	return true
}
