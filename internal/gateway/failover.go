package gateway

// Failover: which failures hand a request to the next candidate, which are
// worth another try of the last one, and how an error that arrives inside an
// event stream — before any of its content — is told from the content.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// How long, and how much, a stream is held back while waiting for its first
// content; past either it is passed through as it comes.
var (
	firstContentWait = 15 * time.Second
	firstContentMax  = 1 << 20
)

// retryWaits are the pauses before the retries of the last candidate: two
// retries, a second and then two. Tests shorten them.
var retryWaits = []time.Duration{time.Second, 2 * time.Second}

// retryCeiling is the longest Retry-After worth waiting for.
const retryCeiling = 8 * time.Second

var (
	// limitWords: money, quota or rate problems that other 4xx answers carry.
	limitWords = regexp.MustCompile(`(?i)quota|insufficient|balance|credit|billing|rate.?limit|usage.?limit|limit.?reached|hit your .*limit|limit.{0,24}resets|too many requests|overloaded|余额|额度|欠费|限流|频率|套餐|用量`)
	// requestWords: the request itself is too big or wrong, whatever else is said.
	requestWords = regexp.MustCompile(`(?i)context.?(length|window)|上下文|too long|too large|maximum.{0,24}length|prompt is too|input.{0,12}too|field required`)
	// missingWords: the model isn't there, isn't allowed, isn't switched on.
	missingWords = regexp.MustCompile(`(?i)model.{0,80}(not (found|exist|support|available|accessible|enabled|allowed|permitted|activated)|does not exist|doesn't exist|unsupported|unavailable|unknown)|(unknown|invalid|unsupported|no such) model|model_not_found|unsupported_api_for_model|not.{0,3}accessible|no access|(does not|doesn't) have access|permission|not authorized|not enabled|not activated|模型.{0,12}(不存在|不支持|无权|未开通|不可用|未找到)|无权|未开通|不存在`)
)

// failoverable says whether another candidate may do better with a request
// that failed this way. Other 400s are the request's own fault.
func failoverable(status int, body []byte) bool {
	switch {
	case status == 401, status == 402, status == 403, status == 404, status == 408, status == 429, status >= 500:
		return true
	case status == 400, status == 422:
		if requestWords.Match(body) {
			return false
		}
		return limitWords.Match(body) || missingWords.Match(body) || wrongEndpoint(status, body)
	}
	return false
}

// retryDelay is whether, and after how long, the last candidate is asked
// again after a failure: only for those that mend themselves, and as long as
// the vendor says when it will.
func retryDelay(attempt, status int, h http.Header) (time.Duration, bool) {
	if attempt >= len(retryWaits) {
		return 0, false
	}
	d, said := explicitRetryAfter(h)
	switch status {
	case 408, 500, 502, 503, 504, 529:
	case 429:
		if !said {
			return 0, false
		}
	default:
		return 0, false
	}
	if !said {
		d = retryWaits[attempt]
	}
	if d > retryCeiling {
		return 0, false
	}
	return d, true
}

// explicitRetryAfter reads the Retry-After header alone: seconds or a date.
func explicitRetryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return max(0, time.Duration(n*float64(time.Second))), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, time.Until(t)), true
	}
	return 0, false
}

type streamStep int

const (
	stepNeutral streamStep = iota // before the content: message_start, ping, a role-only chunk
	stepContent
	stepError
)

// classifyEvent tells what one event of an agent-bound stream is.
func classifyEvent(event, data string) streamStep {
	data = strings.TrimSpace(data)
	if data == "" {
		return stepNeutral
	}
	if data == "[DONE]" {
		return stepContent
	}
	var top map[string]json.RawMessage
	if json.Unmarshal([]byte(data), &top) != nil {
		if event == "error" {
			return stepError
		}
		return stepContent
	}
	var typ string
	json.Unmarshal(top["type"], &typ)
	if event == "error" || typ == "error" || typ == "response.failed" {
		return stepError
	}
	if e, ok := top["error"]; ok && len(top["choices"]) == 0 && string(bytes.TrimSpace(e)) != "null" {
		return stepError
	}
	switch typ {
	case "message_start", "ping", "response.created", "response.in_progress", "response.queued":
		return stepNeutral
	}
	if raw, ok := top["choices"]; ok {
		var choices []struct {
			Delta        map[string]json.RawMessage `json:"delta"`
			FinishReason json.RawMessage            `json:"finish_reason"`
		}
		if json.Unmarshal(raw, &choices) != nil {
			return stepContent
		}
		for _, c := range choices {
			if f := string(bytes.TrimSpace(c.FinishReason)); f != "" && f != "null" {
				return stepContent
			}
			for k, v := range c.Delta {
				if k == "role" {
					continue
				}
				switch s := string(bytes.TrimSpace(v)); s {
				case "", "null", `""`, "[]", "{}":
				default:
					return stepContent
				}
			}
		}
		return stepNeutral
	}
	return stepContent
}

// streamErrorStatus is the status an error event stands for.
func streamErrorStatus(data []byte) int {
	m := strings.ToLower(string(data))
	has := func(ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(m, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("overloaded"):
		return 529
	case has("rate_limit", "rate limit", "too many requests", "quota", "insufficient"):
		return 429
	case has("authentication", "unauthorized", "invalid x-api-key", "invalid api key"):
		return 401
	case has("permission"):
		return 403
	case has("not_found", "not found", "does not exist"):
		return 404
	case has("invalid_request"):
		return 400
	}
	return 500
}
