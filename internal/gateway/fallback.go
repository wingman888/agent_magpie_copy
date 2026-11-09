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
type rsiCandidate struct {
	c     provider.Provider
	model string
}

// candidates lists where a request goes, in order: the provider's enabled
// keys for its model, then the keys of each fallback "provider/model" in the
// order the user listed them. Entries that cannot be read, name no ready
// provider, or repeat an earlier candidate are skipped without a word.
func rsiCandidates(p provider.Provider, from provider.Protocol, model string) []rsiCandidate {
	var out []rsiCandidate
	for _, c := range p.KeyCandidates(from, model) {
		out = append(out, rsiCandidate{c, model})
	}
	seen := map[string]bool{p.ID + "\x00" + model: true}
	for _, entry := range p.Fallback {
		pid, fmodel, ok := rsiSplitFallback(entry)
		if !ok || seen[pid+"\x00"+fmodel] {
			continue
		}
		seen[pid+"\x00"+fmodel] = true
		fp, ok := rsiReadyProvider(pid)
		if !ok {
			continue
		}
		for _, c := range fp.KeyCandidates(from, fmodel) {
			out = append(out, rsiCandidate{c, fmodel})
		}
	}
	return out
}

// splitFallback reads a "provider/model" fallback entry. Provider ids hold no
// slash, so the model is everything after the first one.
func rsiSplitFallback(entry string) (string, string, bool) {
	pid, model, ok := strings.Cut(strings.TrimSpace(entry), "/")
	pid, model = strings.ToLower(strings.TrimSpace(pid)), strings.TrimSpace(model)
	if !ok || pid == "" || model == "" {
		return "", "", false
	}
	return pid, model, true
}

// readyProvider finds a configured provider by id, ready to take requests.
func rsiReadyProvider(id string) (provider.Provider, bool) {
	for _, p := range provider.All() {
		if p.ID == id && p.Ready() {
			return p, true
		}
	}
	return provider.Provider{}, false
}

// retryStatus matches a retryable status code only where the text names it as
// one: "status 429", "HTTP 503", "error 500", or "429 Too Many Requests". A bare
// number does not count, so "max_tokens 5000" or "500 tokens" are not errors.
var rsiRetryStatus = regexp.MustCompile(`(?i)\b(?:status|code|error|http|err)\b\D{0,24}?\b(?:402|429|5\d\d)\b|\b(?:402|429|5\d\d)\s+(?:too many|rate|quota|payment|overload|service|internal|bad gateway|gateway|unavailable|error)`)

// quotaWords are the phrases that say a provider is out of quota, rate
// limited, overloaded or unavailable. Single words such as "insufficient" or
// "balance" are left out: they turn up in ordinary 400 errors too.
var rsiQuotaWords = []string{"rate limit", "rate_limit", "ratelimit", "too many requests", "overload",
	"service unavailable", "temporarily unavailable", "quota", "insufficient_quota", "insufficient quota",
	"insufficient balance", "insufficient_balance", "balance is too low", "credit balance", "exhaust",
	"额度", "余额不足", "用完", "用尽", "限流", "过载"}

// retryText reports whether the text of an error that opened a reply names a
// failure the next candidate should be tried for.
func rsiRetryText(msg string) bool {
	if rsiRetryStatus.MatchString(msg) {
		return true
	}
	m := strings.ToLower(msg)
	for _, w := range rsiQuotaWords {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

const (
	rsiLeadUndecided = iota // not enough of the reply has arrived to tell
	rsiLeadOK               // the reply is not an error: send it
	rsiLeadError            // the reply opens with an error
)

// leadMax is how much of a reply is held back while it is judged.
const rsiLeadMax = 8 << 10

// leadState judges the start of a reply from the complete SSE events in b.
// Control events (ping, message_start, role-only chunks) are skipped; the
// first event that is anything else decides. When the reply is not SSE at
// all, the judgement waits for leadMax bytes.
func rsiLeadState(b []byte) (string, int) {
	rest := b
	for {
		end, n := rsiSseEnd(rest)
		if end < 0 {
			if len(b) >= rsiLeadMax {
				return "", rsiLeadOK
			}
			return "", rsiLeadUndecided
		}
		msg, kind := rsiClassifyEvent(rest[:end])
		switch kind {
		case rsiLeadError:
			return msg, rsiLeadError
		case rsiLeadOK:
			return "", rsiLeadOK
		}
		rest = rest[end+n:]
	}
}

// sseEnd finds the blank line that ends the first SSE event in b, and its
// length.
func rsiSseEnd(b []byte) (int, int) {
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
func rsiClassifyEvent(ev []byte) (string, int) {
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
	if event == "error" || rsiIsErrorData(text) {
		if text == "" {
			text = event
		}
		return text, rsiLeadError
	}
	if len(data) == 0 && event == "" {
		return "", rsiLeadBenign
	}
	switch event {
	case "ping", "message_start", "response.created", "response.in_progress":
		return "", rsiLeadBenign
	}
	if rsiIsBenignData(text) {
		return "", rsiLeadBenign
	}
	return "", rsiLeadOK
}

// leadBenign is a control event that does not yet decide the reply. It sits
// beside the three states leadState reports; it never escapes it.
const rsiLeadBenign = -1

// isErrorData reports whether an event's JSON data is an error.
func rsiIsErrorData(data string) bool {
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
func rsiIsBenignData(data string) bool {
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
